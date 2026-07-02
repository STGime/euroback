package enduser

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/eurobase/euroback/internal/audit"
	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/oauth"
	"github.com/eurobase/euroback/internal/ratelimit"
	"github.com/eurobase/euroback/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// HandleSignUp returns an HTTP handler for POST /v1/auth/signup.
func HandleSignUp(svc *AuthService, limiter ...*ratelimit.RateLimiter) http.HandlerFunc {
	var rl *ratelimit.RateLimiter
	if len(limiter) > 0 {
		rl = limiter[0]
	}
	return func(w http.ResponseWriter, r *http.Request) {
		pc, ok := auth.ProjectFromContext(r.Context())
		if !ok {
			http.Error(w, `{"error":"missing project context"}`, http.StatusUnauthorized)
			return
		}

		config := tenant.ParseAuthConfig(pc.AuthConfig)

		var req SignUpRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}

		// #258 fail-loud pre-check runs BEFORE the rate limiter so a
		// tenant with a broken email-confirmation config doesn't burn
		// their own signup budget on requests we know we're going to
		// 400 anyway. If we consumed the limiter first, a project
		// owner whose SDK integration is stuck on the broken config
		// could lock out real end-users the moment the config gets
		// fixed. The resolver is cheap (map lookup + string compare
		// against redirect_urls), safe to run before the limiter.
		if config.RequireEmailConfirmation {
			if _, ok := config.ResolveEmailRedirect(tenant.EmailFlowVerification, req.EmailRedirectTo); !ok {
				var msg string
				if req.EmailRedirectTo != "" {
					msg = "email_redirect_to must be listed in redirect_urls"
				} else {
					msg = "email confirmation is enabled but auth_config.email_verification_url is not configured (see docs/compliance/tenant-email-flows.md)"
				}
				writeJSON(w, map[string]string{"error": msg}, http.StatusBadRequest)
				return
			}
		}

		// Per-project per-IP volume gate covering signup. Same knob as
		// the signin handler below — overridable via the Rate Limits
		// page (#229). The per-account anti-brute-force gates (e.g.
		// the signin-failure counter keyed by email, the platform-wide
		// resend-verify rate limit) are a separate axis and stay at
		// platform defaults — a project loosening this knob does not
		// loosen those.
		//
		// The IP source honours auth_config.rate_limits.trust_proxy
		// (#228). Default false (Supabase parity, safe under any XFF
		// config); project owners who know their ingress overwrites
		// XFF can opt in to true for true per-end-user keying. See
		// the field doc in internal/tenant/auth_config.go for the
		// trade-off.
		rlCfg := config.EffectiveRateLimits()
		if ratelimit.CheckAuthRateForProject(rl, w, r.Context(), "signup_signin", pc.ProjectID, ratelimit.ClientIPForProject(r, *rlCfg.TrustProxy), rlCfg.SignupSigninPer5MinPerIP, ratelimit.FiveMinutes) {
			return
		}

		resp, err := svc.SignUp(r.Context(), pc.SchemaName, pc.JWTSecret, pc.ProjectID, config, req)
		if err != nil {
			slog.Warn("end-user signup failed", "error", err, "project_id", pc.ProjectID)
			status := http.StatusInternalServerError
			msg := err.Error()
			if msg == "email is required" || strings.HasPrefix(msg, "password must be at least") || msg == "email already registered" || msg == "email/password authentication is disabled" || strings.HasPrefix(msg, "email confirmation is enabled") || strings.HasPrefix(msg, "email_redirect_to must be listed") {
				status = http.StatusBadRequest
			}
			writeJSON(w, map[string]string{"error": msg}, status)
			return
		}

		writeJSON(w, resp, http.StatusCreated)
	}
}

// HandleSignIn returns an HTTP handler for POST /v1/auth/signin.
func HandleSignIn(svc *AuthService, limiter ...*ratelimit.RateLimiter) http.HandlerFunc {
	var rl *ratelimit.RateLimiter
	if len(limiter) > 0 {
		rl = limiter[0]
	}
	return func(w http.ResponseWriter, r *http.Request) {
		pc, ok := auth.ProjectFromContext(r.Context())
		if !ok {
			http.Error(w, `{"error":"missing project context"}`, http.StatusUnauthorized)
			return
		}

		config := tenant.ParseAuthConfig(pc.AuthConfig)

		// Per-project per-IP volume gate covering both signup and
		// signin (#225 — same Supabase knob applies to both). The
		// per-email signin-failure counter below is a separate axis
		// at platform-wide defaults: brute-force per account stays
		// gated even if a project loosens the per-IP knob.
		//
		// IP source honours auth_config.rate_limits.trust_proxy
		// (#228) — default false (TCP peer, safe under any XFF
		// config). See ClientIPForProject for the trade-off; the
		// follow-up issue tracks flipping the default once XFF
		// behavior is verified empirically.
		rlCfg := config.EffectiveRateLimits()
		if ratelimit.CheckAuthRateForProject(rl, w, r.Context(), "signup_signin", pc.ProjectID, ratelimit.ClientIPForProject(r, *rlCfg.TrustProxy), rlCfg.SignupSigninPer5MinPerIP, ratelimit.FiveMinutes) {
			return
		}

		var req SignInRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}

		// Check signin failure rate limit before attempting auth.
		email := strings.ToLower(strings.TrimSpace(req.Email))
		if email != "" && ratelimit.CheckSigninFailRate(rl, w, r.Context(), email) {
			return
		}

		resp, err := svc.SignIn(r.Context(), pc.SchemaName, pc.JWTSecret, pc.ProjectID, config, req)
		if err != nil {
			slog.Warn("end-user signin failed", "error", err, "project_id", pc.ProjectID)
			// Record the failure for rate limiting.
			if email != "" {
				ratelimit.RecordSigninFailure(rl, r.Context(), email)
			}
			// Closes #53: previously a correct password against an
			// unverified account returned 403 email_not_confirmed,
			// which lets an attacker probe both account existence AND
			// password correctness. We now collapse both rejection
			// reasons into the same 401. Verification reminders should
			// be delivered out-of-band (TODO: send a hint email when we
			// know the account exists but is unverified).
			writeJSON(w, map[string]string{"error": "invalid email or password"}, http.StatusUnauthorized)
			return
		}

		writeJSON(w, resp, http.StatusOK)
	}
}

// HandleRefresh returns an HTTP handler for POST /v1/auth/refresh.
func HandleRefresh(svc *AuthService, limiter ...*ratelimit.RateLimiter) http.HandlerFunc {
	var rl *ratelimit.RateLimiter
	if len(limiter) > 0 {
		rl = limiter[0]
	}
	return func(w http.ResponseWriter, r *http.Request) {
		pc, ok := auth.ProjectFromContext(r.Context())
		if !ok {
			http.Error(w, `{"error":"missing project context"}`, http.StatusUnauthorized)
			return
		}

		config := tenant.ParseAuthConfig(pc.AuthConfig)

		// Per-project per-IP gate on the refresh endpoint (#226 —
		// closes the burst-guard gap that #56's reuse-detection
		// can't cover before a stolen token is family-revoked).
		// Default 150 per 5 min — generous because legitimate SDK
		// clients refresh proactively; tighten via the Rate Limits
		// page. IP source honours trust_proxy (#228).
		rlCfg := config.EffectiveRateLimits()
		if ratelimit.CheckAuthRateForProject(rl, w, r.Context(), "token_refresh", pc.ProjectID, ratelimit.ClientIPForProject(r, *rlCfg.TrustProxy), rlCfg.TokenRefreshPer5MinPerIP, ratelimit.FiveMinutes) {
			return
		}

		var req RefreshRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}

		resp, err := svc.RefreshToken(r.Context(), pc.SchemaName, pc.JWTSecret, pc.ProjectID, config, req.RefreshToken)
		if err != nil {
			slog.Warn("token refresh failed", "error", err, "project_id", pc.ProjectID)
			writeJSON(w, map[string]string{"error": "invalid or expired refresh token"}, http.StatusUnauthorized)
			return
		}

		writeJSON(w, resp, http.StatusOK)
	}
}

// HandleSignOut returns an HTTP handler for POST /v1/auth/signout.
func HandleSignOut(svc *AuthService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pc, ok := auth.ProjectFromContext(r.Context())
		if !ok {
			http.Error(w, `{"error":"missing project context"}`, http.StatusUnauthorized)
			return
		}

		var req RefreshRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}

		if err := svc.SignOut(r.Context(), pc.SchemaName, req.RefreshToken); err != nil {
			slog.Error("signout failed", "error", err, "project_id", pc.ProjectID)
		}

		writeJSON(w, map[string]string{"status": "ok"}, http.StatusOK)
	}
}

// HandleGetUser returns an HTTP handler for GET /v1/auth/user.
func HandleGetUser(svc *AuthService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pc, ok := auth.ProjectFromContext(r.Context())
		if !ok {
			http.Error(w, `{"error":"missing project context"}`, http.StatusUnauthorized)
			return
		}

		claims, ok := auth.EndUserClaimsFromContext(r.Context())
		if !ok {
			http.Error(w, `{"error":"authentication required"}`, http.StatusUnauthorized)
			return
		}

		user, err := svc.GetUser(r.Context(), pc.SchemaName, claims.UserID)
		if err != nil {
			slog.Error("get user failed", "error", err, "user_id", claims.UserID)
			if strings.Contains(err.Error(), "not found") {
				writeJSON(w, map[string]string{"error": "user not found"}, http.StatusNotFound)
			} else {
				writeJSON(w, map[string]string{"error": "internal server error"}, http.StatusInternalServerError)
			}
			return
		}

		// GDPR access log: an end-user read their own profile.
		if rec := audit.AccessRecorderFromContext(r.Context()); rec != nil {
			rec.Record(audit.AccessEvent{
				ProjectID:   pc.ProjectID,
				EndUserID:   claims.UserID,
				ActorRole:   "authenticated",
				Action:      audit.AccessActionRead,
				TargetTable: "users",
				TargetKeys:  map[string]interface{}{"id": claims.UserID},
				IP:          audit.ClientIPFromContext(r.Context()),
			})
		}

		writeJSON(w, user, http.StatusOK)
	}
}

// HandleForgotPassword returns an HTTP handler for POST /v1/auth/forgot-password.
func HandleForgotPassword(svc *AuthService, limiter ...*ratelimit.RateLimiter) http.HandlerFunc {
	var rl *ratelimit.RateLimiter
	if len(limiter) > 0 {
		rl = limiter[0]
	}
	return func(w http.ResponseWriter, r *http.Request) {
		pc, ok := auth.ProjectFromContext(r.Context())
		if !ok {
			http.Error(w, `{"error":"missing project context"}`, http.StatusUnauthorized)
			return
		}

		var req struct {
			Email           string `json:"email"`
			EmailRedirectTo string `json:"email_redirect_to,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, map[string]string{"error": "invalid request body"}, http.StatusBadRequest)
			return
		}

		config := tenant.ParseAuthConfig(pc.AuthConfig)

		// Rate limit: 3 per email per 15 min.
		email := strings.ToLower(strings.TrimSpace(req.Email))
		if email != "" && ratelimit.CheckAuthRate(rl, w, r.Context(), "forgot_password", email, ratelimit.ForgotPasswordLimit, ratelimit.ForgotPasswordWindow) {
			return
		}

		// Load project name for email template.
		var projectName string
		_ = svc.pool.QueryRow(r.Context(), `SELECT name FROM projects WHERE id = $1`, pc.ProjectID).Scan(&projectName)

		_ = svc.ForgotPassword(r.Context(), pc.SchemaName, pc.ProjectID, projectName, req.Email, config, req.EmailRedirectTo)

		// Always return 200 to prevent email enumeration.
		writeJSON(w, map[string]string{"status": "ok"}, http.StatusOK)
	}
}

// HandleResetPassword returns an HTTP handler for POST /v1/auth/reset-password.
func HandleResetPassword(svc *AuthService, limiter ...*ratelimit.RateLimiter) http.HandlerFunc {
	var rl *ratelimit.RateLimiter
	if len(limiter) > 0 {
		rl = limiter[0]
	}
	return func(w http.ResponseWriter, r *http.Request) {
		pc, ok := auth.ProjectFromContext(r.Context())
		if !ok {
			http.Error(w, `{"error":"missing project context"}`, http.StatusUnauthorized)
			return
		}

		config := tenant.ParseAuthConfig(pc.AuthConfig)

		// Per-project per-IP gate — shares the token_verify knob with
		// the other verify endpoints. Low risk here (the reset token is
		// high-entropy hex from rand.Read, not enumerable) — added for
		// consistency so a project that tightens token_verify also
		// tightens reset, and one IP can't burn through the bucket via
		// /reset-password while the other verify endpoints stay open.
		rlCfg := config.EffectiveRateLimits()
		if ratelimit.CheckAuthRateForProject(rl, w, r.Context(), "token_verify", pc.ProjectID, ratelimit.ClientIPForProject(r, *rlCfg.TrustProxy), rlCfg.TokenVerificationPer5MinPerIP, ratelimit.FiveMinutes) {
			return
		}

		var req struct {
			Token    string `json:"token"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, map[string]string{"error": "invalid request body"}, http.StatusBadRequest)
			return
		}

		if err := svc.ResetPassword(r.Context(), pc.SchemaName, req.Token, req.Password, config.PasswordMinLength); err != nil {
			slog.Warn("password reset failed", "error", err, "project_id", pc.ProjectID)
			writeJSON(w, map[string]string{"error": err.Error()}, http.StatusBadRequest)
			return
		}

		writeJSON(w, map[string]string{"status": "ok"}, http.StatusOK)
	}
}

// HandleVerifyEmail returns an HTTP handler for POST /v1/auth/verify-email.
func HandleVerifyEmail(svc *AuthService, limiter ...*ratelimit.RateLimiter) http.HandlerFunc {
	var rl *ratelimit.RateLimiter
	if len(limiter) > 0 {
		rl = limiter[0]
	}
	return func(w http.ResponseWriter, r *http.Request) {
		pc, ok := auth.ProjectFromContext(r.Context())
		if !ok {
			http.Error(w, `{"error":"missing project context"}`, http.StatusUnauthorized)
			return
		}

		// Per-project per-IP gate on token-verification endpoints
		// (#226 — defense in depth on top of the per-token controls
		// each underlying flow has). Same knob shared with
		// /signin-magic-link, /phone/verify, and /reset-password.
		//
		// For email-verify, magic-link, and reset-password the token
		// is high-entropy hex from rand.Read — practically
		// unguessable, so the per-IP gate is purely volume control.
		// For phone OTP (6-digit code) the picture is different and
		// the residual risk is not closed by an IP-keyed limit alone:
		// see the security issue tracking the VerifyOTP code-only
		// match + missing per-token attempt counter.
		//
		// IP source honours trust_proxy (#228).
		config := tenant.ParseAuthConfig(pc.AuthConfig)
		rlCfg := config.EffectiveRateLimits()
		if ratelimit.CheckAuthRateForProject(rl, w, r.Context(), "token_verify", pc.ProjectID, ratelimit.ClientIPForProject(r, *rlCfg.TrustProxy), rlCfg.TokenVerificationPer5MinPerIP, ratelimit.FiveMinutes) {
			return
		}

		var req struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, map[string]string{"error": "invalid request body"}, http.StatusBadRequest)
			return
		}

		if err := svc.VerifyEmail(r.Context(), pc.SchemaName, req.Token); err != nil {
			slog.Warn("email verification failed", "error", err, "project_id", pc.ProjectID)
			writeJSON(w, map[string]string{"error": err.Error()}, http.StatusBadRequest)
			return
		}

		writeJSON(w, map[string]string{"status": "ok"}, http.StatusOK)
	}
}

// HandleRequestMagicLink returns an HTTP handler for POST /v1/auth/request-magic-link.
func HandleRequestMagicLink(svc *AuthService, limiter ...*ratelimit.RateLimiter) http.HandlerFunc {
	var rl *ratelimit.RateLimiter
	if len(limiter) > 0 {
		rl = limiter[0]
	}
	return func(w http.ResponseWriter, r *http.Request) {
		pc, ok := auth.ProjectFromContext(r.Context())
		if !ok {
			http.Error(w, `{"error":"missing project context"}`, http.StatusUnauthorized)
			return
		}

		config := tenant.ParseAuthConfig(pc.AuthConfig)

		var req struct {
			Email           string `json:"email"`
			EmailRedirectTo string `json:"email_redirect_to,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, map[string]string{"error": "invalid request body"}, http.StatusBadRequest)
			return
		}

		// Rate limit: 3 per email per 15 min.
		email := strings.ToLower(strings.TrimSpace(req.Email))
		if email != "" && ratelimit.CheckAuthRate(rl, w, r.Context(), "magic_link", email, ratelimit.MagicLinkLimit, ratelimit.MagicLinkWindow) {
			return
		}

		// Load project name for email template.
		var projectName string
		_ = svc.pool.QueryRow(r.Context(), `SELECT name FROM projects WHERE id = $1`, pc.ProjectID).Scan(&projectName)

		err := svc.RequestMagicLink(r.Context(), pc.SchemaName, pc.ProjectID, projectName, config, req.Email, req.EmailRedirectTo)
		if err != nil {
			slog.Warn("request-magic-link failed", "error", err, "project_id", pc.ProjectID)
		}

		// Always return 200 to prevent email enumeration.
		writeJSON(w, map[string]string{"status": "ok"}, http.StatusOK)
	}
}

// HandleSignInWithMagicLink returns an HTTP handler for POST /v1/auth/signin-magic-link.
func HandleSignInWithMagicLink(svc *AuthService, limiter ...*ratelimit.RateLimiter) http.HandlerFunc {
	var rl *ratelimit.RateLimiter
	if len(limiter) > 0 {
		rl = limiter[0]
	}
	return func(w http.ResponseWriter, r *http.Request) {
		pc, ok := auth.ProjectFromContext(r.Context())
		if !ok {
			http.Error(w, `{"error":"missing project context"}`, http.StatusUnauthorized)
			return
		}

		config := tenant.ParseAuthConfig(pc.AuthConfig)

		// Per-project per-IP gate — shares the token_verify knob with
		// the email and phone OTP verify endpoints. See HandleVerifyEmail
		// for the brute-force rationale. IP source honours
		// trust_proxy (#228).
		rlCfg := config.EffectiveRateLimits()
		if ratelimit.CheckAuthRateForProject(rl, w, r.Context(), "token_verify", pc.ProjectID, ratelimit.ClientIPForProject(r, *rlCfg.TrustProxy), rlCfg.TokenVerificationPer5MinPerIP, ratelimit.FiveMinutes) {
			return
		}

		var req struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, map[string]string{"error": "invalid request body"}, http.StatusBadRequest)
			return
		}

		resp, err := svc.SignInWithMagicLink(r.Context(), pc.SchemaName, pc.JWTSecret, pc.ProjectID, config, req.Token)
		if err != nil {
			slog.Warn("magic-link signin failed", "error", err, "project_id", pc.ProjectID)
			writeJSON(w, map[string]string{"error": err.Error()}, http.StatusBadRequest)
			return
		}

		writeJSON(w, resp, http.StatusOK)
	}
}

// HandleResendVerification returns an HTTP handler for POST /v1/auth/resend-verification.
func HandleResendVerification(svc *AuthService, limiter ...*ratelimit.RateLimiter) http.HandlerFunc {
	var rl *ratelimit.RateLimiter
	if len(limiter) > 0 {
		rl = limiter[0]
	}
	return func(w http.ResponseWriter, r *http.Request) {
		pc, ok := auth.ProjectFromContext(r.Context())
		if !ok {
			http.Error(w, `{"error":"missing project context"}`, http.StatusUnauthorized)
			return
		}

		var req struct {
			Email           string `json:"email"`
			EmailRedirectTo string `json:"email_redirect_to,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, map[string]string{"error": "invalid request body"}, http.StatusBadRequest)
			return
		}

		config := tenant.ParseAuthConfig(pc.AuthConfig)

		// Rate limit: 1 per email per 5 min.
		email := strings.ToLower(strings.TrimSpace(req.Email))
		if email != "" && ratelimit.CheckAuthRate(rl, w, r.Context(), "resend_verify", email, ratelimit.ResendVerifyLimit, ratelimit.ResendVerifyWindow) {
			return
		}

		var projectName string
		_ = svc.pool.QueryRow(r.Context(), `SELECT name FROM projects WHERE id = $1`, pc.ProjectID).Scan(&projectName)

		_ = svc.ResendVerification(r.Context(), pc.SchemaName, pc.ProjectID, projectName, req.Email, config, req.EmailRedirectTo)

		writeJSON(w, map[string]string{"status": "ok"}, http.StatusOK)
	}
}

// HandleOAuthRedirect returns an HTTP handler for GET /v1/auth/oauth/{provider}.
// Generates a state token, builds the auth URL, and redirects the browser to the provider.
func HandleOAuthRedirect(svc *AuthService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		providerName := chi.URLParam(r, "provider")

		pc, ok := auth.ProjectFromContext(r.Context())
		if !ok {
			http.Error(w, `{"error":"missing project context"}`, http.StatusUnauthorized)
			return
		}

		clientRedirectURL := r.URL.Query().Get("redirect_url")
		if clientRedirectURL == "" {
			writeJSON(w, map[string]string{"error": "redirect_url query parameter is required"}, http.StatusBadRequest)
			return
		}

		config := tenant.ParseAuthConfig(pc.AuthConfig)

		// Validate redirect_url against allowed list.
		if !config.IsRedirectURLAllowed(clientRedirectURL) {
			writeJSON(w, map[string]string{"error": "redirect_url is not in the allowed redirect URLs"}, http.StatusBadRequest)
			return
		}

		providerConfig, ok := config.GetOAuthProvider(providerName)
		if !ok {
			writeJSON(w, map[string]string{"error": fmt.Sprintf("oauth provider %q is not enabled", providerName)}, http.StatusBadRequest)
			return
		}

		// Generate state token for CSRF protection. Stored server-side so the
		// callback can validate it as single-use and bound to this project.
		state, err := generateRandomHex(32)
		if err != nil {
			slog.Error("oauth: failed to generate state", "error", err)
			writeJSON(w, map[string]string{"error": "internal server error"}, http.StatusInternalServerError)
			return
		}
		if err := storeOAuthState(r.Context(), svc.pool, state, pc.ProjectID, providerName, clientRedirectURL); err != nil {
			slog.Error("oauth: failed to store state", "error", err)
			writeJSON(w, map[string]string{"error": "internal server error"}, http.StatusInternalServerError)
			return
		}

		// Callback URL is on the Eurobase gateway; only the opaque state goes
		// to the provider. Redirect URL is NEVER roundtripped through state.
		callbackURL := fmt.Sprintf("%s://%s/v1/auth/oauth/%s/callback", schemeFromRequest(r), r.Host, providerName)

		provider, err := oauth.Get(providerName)
		if err != nil {
			writeJSON(w, map[string]string{"error": err.Error()}, http.StatusBadRequest)
			return
		}

		authURL := provider.AuthURL(oauth.AuthURLConfig{
			ClientID:    providerConfig.ClientID,
			RedirectURL: callbackURL,
			State:       state,
			TenantID:    providerConfig.TenantID,
		})
		http.Redirect(w, r, authURL, http.StatusFound)
	}
}

// HandleOAuthCallback returns an HTTP handler for the OAuth callback.
// Supports both GET (standard providers) and POST (Apple's form_post response mode).
// Exchanges the authorization code for user info, creates/finds the user, and redirects
// back to the client app with tokens in the URL fragment.
func HandleOAuthCallback(svc *AuthService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		providerName := chi.URLParam(r, "provider")

		pc, ok := auth.ProjectFromContext(r.Context())
		if !ok {
			http.Error(w, `{"error":"missing project context"}`, http.StatusUnauthorized)
			return
		}

		config := tenant.ParseAuthConfig(pc.AuthConfig)

		// Apple uses response_mode=form_post, so the callback comes as POST
		// with form data. Other providers use GET with query params.
		var code, state string
		if r.Method == http.MethodPost {
			if err := r.ParseForm(); err != nil {
				writeJSON(w, map[string]string{"error": "invalid form data"}, http.StatusBadRequest)
				return
			}
			code = r.FormValue("code")
			state = r.FormValue("state")
		} else {
			code = r.URL.Query().Get("code")
			state = r.URL.Query().Get("state")
		}

		if code == "" {
			// Provider returned an error.
			var errMsg string
			if r.Method == http.MethodPost {
				errMsg = r.FormValue("error")
			} else {
				errMsg = r.URL.Query().Get("error_description")
				if errMsg == "" {
					errMsg = r.URL.Query().Get("error")
				}
			}
			if errMsg == "" {
				errMsg = "missing authorization code"
			}
			slog.Warn("oauth callback: no code", "provider", providerName, "error", errMsg)
			writeJSON(w, map[string]string{"error": errMsg}, http.StatusBadRequest)
			return
		}

		// Validate state: must exist, be unexpired, bound to this project and
		// provider. Consuming deletes the row so replay is impossible.
		if state == "" {
			writeJSON(w, map[string]string{"error": "missing state parameter"}, http.StatusBadRequest)
			return
		}
		rec, err := consumeOAuthState(r.Context(), svc.pool, state)
		if err != nil {
			slog.Warn("oauth callback: invalid state", "error", err, "provider", providerName, "project_id", pc.ProjectID)
			writeJSON(w, map[string]string{"error": "invalid or expired state"}, http.StatusBadRequest)
			return
		}
		if rec.ProjectID != pc.ProjectID || rec.Provider != providerName {
			slog.Warn("oauth callback: state/context mismatch",
				"state_project", rec.ProjectID, "ctx_project", pc.ProjectID,
				"state_provider", rec.Provider, "url_provider", providerName)
			writeJSON(w, map[string]string{"error": "invalid state"}, http.StatusBadRequest)
			return
		}
		clientRedirectURL := rec.RedirectURL

		// Re-validate redirect_url against the allowlist — config may have changed
		// between redirect and callback.
		if !config.IsRedirectURLAllowed(clientRedirectURL) {
			writeJSON(w, map[string]string{"error": "redirect_url is not in the allowed redirect URLs"}, http.StatusBadRequest)
			return
		}

		// Build the callback URL that was used for the token exchange.
		callbackURL := fmt.Sprintf("%s://%s/v1/auth/oauth/%s/callback", schemeFromRequest(r), r.Host, providerName)

		resp, err := svc.SignInWithOAuth(r.Context(), pc.SchemaName, pc.JWTSecret, pc.ProjectID, config, providerName, code, callbackURL)
		if err != nil {
			slog.Warn("oauth signin failed", "error", err, "provider", providerName, "project_id", pc.ProjectID)
			// Closed advisory GHSA-269x-fqhj-x9jq: when OAuth signin
			// resolves to an existing user by email but no provider
			// identity is linked, refuse to auto-link. Surface a
			// distinct error code so client apps can guide the user
			// to sign in with their existing credentials and link
			// the provider from settings.
			if errors.Is(err, ErrAccountExistsLinkRequired) {
				redirectWithError(w, r, clientRedirectURL, "account_exists_link_required", err.Error())
				return
			}
			redirectWithError(w, r, clientRedirectURL, "oauth_error", err.Error())
			return
		}

		// Redirect to client app with tokens in URL fragment.
		fragment := url.Values{
			"access_token":  {resp.AccessToken},
			"refresh_token": {resp.RefreshToken},
			"token_type":    {resp.TokenType},
			"expires_in":    {fmt.Sprintf("%d", resp.ExpiresIn)},
		}
		redirectURL := clientRedirectURL + "#" + fragment.Encode()
		http.Redirect(w, r, redirectURL, http.StatusFound)
	}
}

// schemeFromRequest determines the scheme (http or https) from the request.
func schemeFromRequest(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		return proto
	}
	return "http"
}

// redirectWithError redirects to the client redirect URL with error query params.
func redirectWithError(w http.ResponseWriter, r *http.Request, redirectURL, errCode, errDesc string) {
	u, err := url.Parse(redirectURL)
	if err != nil {
		http.Error(w, `{"error":"invalid redirect URL"}`, http.StatusBadRequest)
		return
	}
	q := u.Query()
	q.Set("error", errCode)
	q.Set("error_description", errDesc)
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

// HandleSendPhoneOTP returns an HTTP handler for POST /v1/auth/phone/send-otp.
func HandleSendPhoneOTP(svc *AuthService, limiter ...*ratelimit.RateLimiter) http.HandlerFunc {
	var rl *ratelimit.RateLimiter
	if len(limiter) > 0 {
		rl = limiter[0]
	}
	return func(w http.ResponseWriter, r *http.Request) {
		pc, ok := auth.ProjectFromContext(r.Context())
		if !ok {
			http.Error(w, `{"error":"missing project context"}`, http.StatusUnauthorized)
			return
		}

		config := tenant.ParseAuthConfig(pc.AuthConfig)
		if !config.IsPhoneAuthEnabled() {
			writeJSON(w, map[string]string{"error": "phone authentication is not enabled"}, http.StatusBadRequest)
			return
		}

		var req SendPhoneOTPRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, map[string]string{"error": "invalid request body"}, http.StatusBadRequest)
			return
		}
		if req.Phone == "" {
			writeJSON(w, map[string]string{"error": "phone is required"}, http.StatusBadRequest)
			return
		}

		// Rate limit: 3 per phone per 15 min.
		if ratelimit.CheckAuthRate(rl, w, r.Context(), "phone_otp", req.Phone, ratelimit.PhoneOTPLimit, ratelimit.PhoneOTPWindow) {
			return
		}

		if err := svc.SendPhoneOTP(r.Context(), pc.SchemaName, pc.ProjectID, req.Phone, config); err != nil {
			slog.Warn("send phone otp failed", "error", err)
			writeJSON(w, map[string]string{"error": err.Error()}, http.StatusBadRequest)
			return
		}

		writeJSON(w, map[string]string{"status": "otp_sent"}, http.StatusOK)
	}
}

// HandleVerifyPhoneOTP returns an HTTP handler for POST /v1/auth/phone/verify.
func HandleVerifyPhoneOTP(svc *AuthService, limiter ...*ratelimit.RateLimiter) http.HandlerFunc {
	var rl *ratelimit.RateLimiter
	if len(limiter) > 0 {
		rl = limiter[0]
	}
	return func(w http.ResponseWriter, r *http.Request) {
		pc, ok := auth.ProjectFromContext(r.Context())
		if !ok {
			http.Error(w, `{"error":"missing project context"}`, http.StatusUnauthorized)
			return
		}

		config := tenant.ParseAuthConfig(pc.AuthConfig)
		if !config.IsPhoneAuthEnabled() {
			writeJSON(w, map[string]string{"error": "phone authentication is not enabled"}, http.StatusBadRequest)
			return
		}

		// Per-project per-IP gate — shares the token_verify knob with
		// the email and magic-link verify endpoints. This NARROWS but
		// does not CLOSE phone-OTP brute force: the underlying
		// VerifyOTP SQL matches the 6-digit code tenant-wide
		// (`WHERE token_hash = sha256(code)`) with no per-token
		// attempt counter, so a forged XFF or a modest botnet can
		// still chip away at the 10^6 code space. The proper fix is
		// tracked as a separate P1 security issue: bind verify to the
		// phone (`WHERE phone = $1 AND token_hash = $2`) + a
		// per-token attempt counter. The per-phone OTP-issue limit
		// (PhoneOTPLimit) gates the SEND side, not the verify.
		// IP source honours trust_proxy (#228).
		rlCfg := config.EffectiveRateLimits()
		if ratelimit.CheckAuthRateForProject(rl, w, r.Context(), "token_verify", pc.ProjectID, ratelimit.ClientIPForProject(r, *rlCfg.TrustProxy), rlCfg.TokenVerificationPer5MinPerIP, ratelimit.FiveMinutes) {
			return
		}

		var req VerifyPhoneOTPRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, map[string]string{"error": "invalid request body"}, http.StatusBadRequest)
			return
		}
		if req.Phone == "" || req.Code == "" {
			writeJSON(w, map[string]string{"error": "phone and code are required"}, http.StatusBadRequest)
			return
		}

		resp, err := svc.VerifyPhoneOTP(r.Context(), pc.SchemaName, pc.JWTSecret, pc.ProjectID, config, req.Phone, req.Code)
		if err != nil {
			slog.Warn("verify phone otp failed", "error", err)
			writeJSON(w, map[string]string{"error": err.Error()}, http.StatusBadRequest)
			return
		}

		writeJSON(w, resp, http.StatusOK)
	}
}

func writeJSON(w http.ResponseWriter, v interface{}, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
