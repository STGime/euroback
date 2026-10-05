package enduser

// SDK end-user passkey endpoints (#630, PR 3). Mounted under /v1/auth:
//   - POST /passkey/login/begin   (public key only; discoverable)
//   - POST /passkey/login/finish  (public key only) → session
//   - POST /passkey/register/begin  (end-user JWT required)
//   - POST /passkey/register/finish (end-user JWT required)
//   - GET  /passkey                 (end-user JWT required) — list
//   - DELETE /passkey/{id}          (end-user JWT required)
//
// register/list/delete require a valid end-user access token (short-lived),
// so a stolen long-lived session can't silently enrol an attacker passkey.
// login/* are keyed to the PROJECT's own JWT secret (pc.JWTSecret).

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/ratelimit"
	"github.com/eurobase/euroback/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// passkeyRegisterMaxBody matches the console's cap (internal/auth) — a
// WebAuthn attestation response can be several KB (TPM / x5c chains), so
// 16KB can trip legitimate enrolment on some authenticators.
const passkeyRegisterMaxBody = 64 << 10

// passkeyLoginRateLimit applies the per-project per-source-IP sign-in ceiling
// (shared knob with signup/signin) to the public passkey login endpoints —
// they're unauthenticated and login/finish opens a DB tx, so they get the
// same throttle every sibling auth endpoint has. Returns true if blocked
// (response written). Nil limiter = fail open (dev / Redis down).
func passkeyLoginRateLimit(limiter *ratelimit.RateLimiter, w http.ResponseWriter, r *http.Request, pc *auth.ProjectContext, config tenant.AuthConfig) bool {
	rlCfg := config.EffectiveRateLimits()
	return ratelimit.CheckAuthRateForProject(limiter, w, r.Context(), "passkey_login", pc.ProjectID,
		ratelimit.ClientIPForProject(r, *rlCfg.TrustProxy, *rlCfg.TrustedProxyHops),
		rlCfg.SignupSigninPer5MinPerIP, ratelimit.FiveMinutes)
}

// writePasskeyError maps a ceremony error to an HTTP status without leaking
// which step failed (the detail is logged, not returned).
func writePasskeyError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrPasskeysUnavailable):
		writeJSON(w, map[string]string{"error": "passkeys are not enabled for this project"}, http.StatusBadRequest)
	case errors.Is(err, auth.ErrPasskeyChallengeInvalid), errors.Is(err, auth.ErrPasskeyVerificationFailed):
		writeJSON(w, map[string]string{"error": err.Error()}, http.StatusBadRequest)
	case errors.Is(err, auth.ErrPasskeyLimitReached):
		writeJSON(w, map[string]string{"error": err.Error()}, http.StatusConflict)
	case errors.Is(err, auth.ErrPasskeyAlreadyRegistered):
		writeJSON(w, map[string]string{"error": err.Error()}, http.StatusConflict)
	case errors.Is(err, auth.ErrPasskeyNotFound):
		writeJSON(w, map[string]string{"error": "passkey not found"}, http.StatusNotFound)
	case errors.Is(err, auth.ErrPasskeyNicknameInvalid):
		writeJSON(w, map[string]string{"error": err.Error()}, http.StatusBadRequest)
	case err != nil && err.Error() == "email_not_confirmed":
		writeJSON(w, map[string]string{"error": "email not confirmed"}, http.StatusForbidden)
	default:
		writeJSON(w, map[string]string{"error": "internal server error"}, http.StatusInternalServerError)
	}
}

// authedPasskeyCtx pulls the project + authenticated end user from context.
// NOTE: EndUserMiddleware is OPTIONAL (it passes anonymously when no
// Authorization header is present), so THIS check — not the middleware — is
// the auth gate for the manage endpoints: no end-user claims → 401. Any new
// manage handler MUST call this (or otherwise require claims).
func authedPasskeyCtx(w http.ResponseWriter, r *http.Request) (pc *auth.ProjectContext, claims *auth.EndUserClaims, config tenant.AuthConfig, ok bool) {
	pc, got := auth.ProjectFromContext(r.Context())
	if !got || pc == nil {
		http.Error(w, `{"error":"missing project context"}`, http.StatusUnauthorized)
		return nil, nil, config, false
	}
	claims, got = auth.EndUserClaimsFromContext(r.Context())
	if !got {
		http.Error(w, `{"error":"authentication required"}`, http.StatusUnauthorized)
		return nil, nil, config, false
	}
	return pc, claims, tenant.ParseAuthConfig(pc.AuthConfig), true
}

// HandlePasskeyRegisterBegin — POST /v1/auth/passkey/register/begin (authed).
func HandlePasskeyRegisterBegin(svc *AuthService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pc, claims, config, ok := authedPasskeyCtx(w, r)
		if !ok {
			return
		}
		ch, err := svc.BeginPasskeyRegistration(r.Context(), pc.SchemaName, config, claims.UserID, claims.Email)
		if err != nil {
			slog.Info("passkey register begin failed", "error", err, "project_id", pc.ProjectID)
			writePasskeyError(w, err)
			return
		}
		writeJSON(w, ch, http.StatusOK)
	}
}

// HandlePasskeyRegisterFinish — POST /v1/auth/passkey/register/finish (authed).
func HandlePasskeyRegisterFinish(svc *AuthService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pc, claims, config, ok := authedPasskeyCtx(w, r)
		if !ok {
			return
		}
		var req struct {
			ChallengeID string          `json:"challenge_id"`
			Credential  json.RawMessage `json:"credential"`
			Nickname    string          `json:"nickname"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, passkeyRegisterMaxBody)).Decode(&req); err != nil || req.ChallengeID == "" || len(req.Credential) == 0 {
			writeJSON(w, map[string]string{"error": "invalid request body"}, http.StatusBadRequest)
			return
		}
		info, err := svc.FinishPasskeyRegistration(r.Context(), pc.SchemaName, config, claims.UserID, req.ChallengeID, req.Credential, req.Nickname)
		if err != nil {
			slog.Info("passkey register finish failed", "error", err, "project_id", pc.ProjectID)
			writePasskeyError(w, err)
			return
		}
		writeJSON(w, info, http.StatusOK)
	}
}

// HandlePasskeyLoginBegin — POST /v1/auth/passkey/login/begin (public key only).
func HandlePasskeyLoginBegin(svc *AuthService, limiter *ratelimit.RateLimiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pc, ok := auth.ProjectFromContext(r.Context())
		if !ok || pc == nil {
			http.Error(w, `{"error":"missing project context"}`, http.StatusUnauthorized)
			return
		}
		config := tenant.ParseAuthConfig(pc.AuthConfig)
		if passkeyLoginRateLimit(limiter, w, r, pc, config) {
			return
		}
		ch, err := svc.BeginPasskeyLogin(r.Context(), config, []byte(pc.JWTSecret))
		if err != nil {
			writePasskeyError(w, err)
			return
		}
		writeJSON(w, ch, http.StatusOK)
	}
}

// HandlePasskeyLoginFinish — POST /v1/auth/passkey/login/finish → session.
func HandlePasskeyLoginFinish(svc *AuthService, limiter *ratelimit.RateLimiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pc, ok := auth.ProjectFromContext(r.Context())
		if !ok || pc == nil {
			http.Error(w, `{"error":"missing project context"}`, http.StatusUnauthorized)
			return
		}
		config := tenant.ParseAuthConfig(pc.AuthConfig)
		if passkeyLoginRateLimit(limiter, w, r, pc, config) {
			return
		}
		var req struct {
			ChallengeID string          `json:"challenge_id"`
			Credential  json.RawMessage `json:"credential"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024)).Decode(&req); err != nil || req.ChallengeID == "" || len(req.Credential) == 0 {
			writeJSON(w, map[string]string{"error": "invalid request body"}, http.StatusBadRequest)
			return
		}
		resp, err := svc.CompletePasskeyLogin(r.Context(), pc.SchemaName, pc.JWTSecret, pc.ProjectID, config, req.ChallengeID, req.Credential)
		if err != nil {
			slog.Info("passkey login failed", "error", err, "project_id", pc.ProjectID)
			writePasskeyError(w, err)
			return
		}
		writeJSON(w, resp, http.StatusOK)
	}
}

// HandleListPasskeys — GET /v1/auth/passkey (authed).
func HandleListPasskeys(svc *AuthService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pc, claims, _, ok := authedPasskeyCtx(w, r)
		if !ok {
			return
		}
		list, err := svc.ListPasskeys(r.Context(), pc.SchemaName, claims.UserID)
		if err != nil {
			slog.Error("passkey list failed", "error", err, "project_id", pc.ProjectID)
			writePasskeyError(w, err)
			return
		}
		writeJSON(w, map[string]any{"passkeys": list}, http.StatusOK)
	}
}

// HandleDeletePasskey — DELETE /v1/auth/passkey/{id} (authed).
func HandleDeletePasskey(svc *AuthService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pc, claims, _, ok := authedPasskeyCtx(w, r)
		if !ok {
			return
		}
		if err := svc.DeletePasskey(r.Context(), pc.SchemaName, claims.UserID, chi.URLParam(r, "id")); err != nil {
			writePasskeyError(w, err)
			return
		}
		writeJSON(w, map[string]string{"status": "ok"}, http.StatusOK)
	}
}
