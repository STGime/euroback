package auth

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// Passkey HTTP handlers (#621). Unauthenticated sign-in routes live
// under /platform/auth/passkey/*, enrolment / management under the
// authenticated /platform/auth/account/passkeys/* subtree.

// maxPasskeyBody bounds the JSON body of every passkey POST. A WebAuthn
// attestation response is a few KB; 64 KB leaves ample headroom.
const maxPasskeyBody = 64 << 10

func passkeyMeta(r *http.Request) PasskeyRequestMeta {
	return PasskeyRequestMeta{IP: clientIP(r), UserAgent: r.UserAgent()}
}

func decodePasskeyBody(w http.ResponseWriter, r *http.Request, v interface{}) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxPasskeyBody))
	if err != nil {
		writeJSONError(w, "request body too large", http.StatusRequestEntityTooLarge)
		return false
	}
	if err := json.Unmarshal(body, v); err != nil {
		writeJSONError(w, "invalid request body", http.StatusBadRequest)
		return false
	}
	return true
}

func writeJSONErrorCode(w http.ResponseWriter, msg, code string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg, "code": code})
}

// writePasskeyError maps passkey sentinels to status codes.
func writePasskeyError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrPasskeysUnavailable):
		writeJSONErrorCode(w, err.Error(), "passkeys_unavailable", http.StatusServiceUnavailable)
	case errors.Is(err, ErrReauthRequired):
		writeJSONErrorCode(w, err.Error(), "reauth_required", http.StatusForbidden)
	case errors.Is(err, ErrPasskeyVerificationFailed):
		writeJSONErrorCode(w, err.Error(), "passkey_verification_failed", http.StatusUnauthorized)
	case errors.Is(err, ErrPasskeyChallengeInvalid):
		writeJSONErrorCode(w, err.Error(), "passkey_challenge_invalid", http.StatusBadRequest)
	case errors.Is(err, ErrEmailNotVerified):
		writeJSONErrorCode(w, "Please verify your email before signing in — check your inbox for the confirmation link.", "email_not_verified", http.StatusForbidden)
	case errors.Is(err, ErrPasskeyNotFound):
		writeJSONError(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, ErrPasskeyAlreadyRegistered):
		writeJSONError(w, err.Error(), http.StatusConflict)
	case errors.Is(err, ErrPasskeyLimitReached), errors.Is(err, ErrPasskeyNicknameInvalid):
		writeJSONError(w, err.Error(), http.StatusBadRequest)
	default:
		slog.Error("passkey request failed", "error", err)
		writeJSONError(w, "internal error", http.StatusInternalServerError)
	}
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// ── Sign-in (unauthenticated) ──────────────────────────────────────

// HandlePasskeyLoginBegin — POST /platform/auth/passkey/login/begin.
// Stateless (signed challenge token, no DB write) — deliberately not
// rate-limited: behind the LB a per-IP limit is product-wide, so one
// client could lock everyone out of passkey sign-in.
func HandlePasskeyLoginBegin(svc *PlatformAuthService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ch, err := svc.BeginPasskeyLogin(r.Context())
		if err != nil {
			writePasskeyError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, ch)
	}
}

type passkeyFinishRequest struct {
	ChallengeID string          `json:"challenge_id"`
	Credential  json.RawMessage `json:"credential"`
}

// HandlePasskeyLoginFinish — POST /platform/auth/passkey/login/finish.
func HandlePasskeyLoginFinish(svc *PlatformAuthService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req passkeyFinishRequest
		if !decodePasskeyBody(w, r, &req) {
			return
		}
		resp, err := svc.FinishPasskeyLogin(r.Context(), req.ChallengeID, req.Credential, passkeyMeta(r))
		if err != nil {
			writePasskeyError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// HandlePasskeyStepUp — POST /platform/auth/passkey/step-up. Completes
// password → passkey sign-in with the mfa_token SignIn returned.
func HandlePasskeyStepUp(svc *PlatformAuthService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			MFAToken   string          `json:"mfa_token"`
			Credential json.RawMessage `json:"credential"`
		}
		if !decodePasskeyBody(w, r, &req) {
			return
		}
		resp, err := svc.FinishStepUp(r.Context(), req.MFAToken, req.Credential, passkeyMeta(r))
		if err != nil {
			writePasskeyError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// ── Enrolment / management (authenticated) ─────────────────────────

// requireConsoleSession returns the claims for a console JWT session.
// PATs are refused: a passkey is a full console login credential, so a
// leaked CLI / MCP token must not be able to mint one (or remove the
// owner's).
func requireConsoleSession(w http.ResponseWriter, r *http.Request) (*Claims, bool) {
	claims, ok := ClaimsFromContext(r.Context())
	if !ok {
		writeJSONError(w, "unauthorized", http.StatusUnauthorized)
		return nil, false
	}
	if isPATAuth(r) {
		writeJSONError(w, "personal access tokens cannot manage passkeys; sign in to the console", http.StatusForbidden)
		return nil, false
	}
	return claims, true
}

// checkPasskeyReauth applies the re-auth rule, counting wrong passwords
// against the same per-account signin_fail budget as the login form so
// a stolen (older) session can't brute-force the password here.
func checkPasskeyReauth(w http.ResponseWriter, r *http.Request, svc *PlatformAuthService, claims *Claims, password string, check AuthRateLimiter) bool {
	key := "platform:" + strings.ToLower(strings.TrimSpace(claims.Email))
	if password != "" && check != nil && check(w, r, "signin_fail", key) {
		return false
	}
	if err := svc.CheckPasskeyReauth(r.Context(), claims, password); err != nil {
		if errors.Is(err, ErrReauthRequired) && password != "" && check != nil {
			check(w, r, "signin_fail_record", key)
		}
		writePasskeyError(w, err)
		return false
	}
	return true
}

// HandleListPasskeys — GET /platform/auth/account/passkeys.
func HandleListPasskeys(svc *PlatformAuthService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireConsoleSession(w, r)
		if !ok {
			return
		}
		list, err := svc.ListPasskeys(r.Context(), claims.Subject)
		if err != nil {
			writePasskeyError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"passkeys":    list,
			"mfa_enabled": len(list) > 0,
		})
	}
}

// HandlePasskeyRegisterBegin — POST /platform/auth/account/passkeys/register/begin.
// Body: {"current_password": "..."} — required unless the session is
// fresh (see passkeyReauthWindow).
func HandlePasskeyRegisterBegin(svc *PlatformAuthService, rateFn ...AuthRateLimiter) http.HandlerFunc {
	var check AuthRateLimiter
	if len(rateFn) > 0 {
		check = rateFn[0]
	}
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireConsoleSession(w, r)
		if !ok {
			return
		}
		var req struct {
			CurrentPassword string `json:"current_password"`
		}
		if !decodePasskeyBody(w, r, &req) {
			return
		}
		if !checkPasskeyReauth(w, r, svc, claims, req.CurrentPassword, check) {
			return
		}
		ch, err := svc.BeginPasskeyRegistration(r.Context(), claims.Subject)
		if err != nil {
			writePasskeyError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, ch)
	}
}

// HandlePasskeyRegisterFinish — POST /platform/auth/account/passkeys/register/finish.
// The re-auth check happened at begin; the challenge is bound to the
// user and single-use, so finish doesn't repeat it.
func HandlePasskeyRegisterFinish(svc *PlatformAuthService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireConsoleSession(w, r)
		if !ok {
			return
		}
		var req struct {
			passkeyFinishRequest
			Nickname string `json:"nickname"`
		}
		if !decodePasskeyBody(w, r, &req) {
			return
		}
		info, err := svc.FinishPasskeyRegistration(r.Context(), claims.Subject, req.ChallengeID, req.Credential, req.Nickname, passkeyMeta(r))
		if err != nil {
			writePasskeyError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, info)
	}
}

// HandleRenamePasskey — PATCH /platform/auth/account/passkeys/{id}.
func HandleRenamePasskey(svc *PlatformAuthService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireConsoleSession(w, r)
		if !ok {
			return
		}
		var req struct {
			Nickname string `json:"nickname"`
		}
		if !decodePasskeyBody(w, r, &req) {
			return
		}
		if err := svc.RenamePasskey(r.Context(), claims.Subject, chi.URLParam(r, "id"), req.Nickname); err != nil {
			writePasskeyError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// HandleDeletePasskey — POST /platform/auth/account/passkeys/{id}/delete.
// POST (not DELETE) because it carries a body: the re-auth password.
func HandleDeletePasskey(svc *PlatformAuthService, rateFn ...AuthRateLimiter) http.HandlerFunc {
	var check AuthRateLimiter
	if len(rateFn) > 0 {
		check = rateFn[0]
	}
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireConsoleSession(w, r)
		if !ok {
			return
		}
		var req struct {
			CurrentPassword string `json:"current_password"`
		}
		if !decodePasskeyBody(w, r, &req) {
			return
		}
		if !checkPasskeyReauth(w, r, svc, claims, req.CurrentPassword, check) {
			return
		}
		if err := svc.DeletePasskey(r.Context(), claims.Subject, claims.Email, chi.URLParam(r, "id"), passkeyMeta(r)); err != nil {
			writePasskeyError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
