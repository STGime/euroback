package enduser

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

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

		// Rate limit signups by IP: 5/hour.
		if ratelimit.CheckAuthRate(rl, w, r.Context(), "signup", ratelimit.ClientIP(r), ratelimit.SignupLimit, ratelimit.SignupWindow) {
			return
		}

		config := tenant.ParseAuthConfig(pc.AuthConfig)

		var req SignUpRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}

		resp, err := svc.SignUp(r.Context(), pc.SchemaName, pc.JWTSecret, pc.ProjectID, config, req)
		if err != nil {
			slog.Warn("end-user signup failed", "error", err, "project_id", pc.ProjectID)
			status := http.StatusInternalServerError
			msg := err.Error()
			if msg == "email is required" || strings.HasPrefix(msg, "password must be at least") || msg == "email already registered" || msg == "email/password authentication is disabled" {
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
			if err.Error() == "email_not_confirmed" {
				writeJSON(w, map[string]string{"error": "email_not_confirmed", "message": "Please verify your email address before signing in. Check your inbox."}, http.StatusForbidden)
				return
			}
			writeJSON(w, map[string]string{"error": "invalid email or password"}, http.StatusUnauthorized)
			return
		}

		writeJSON(w, resp, http.StatusOK)
	}
}

// HandleRefresh returns an HTTP handler for POST /v1/auth/refresh.
func HandleRefresh(svc *AuthService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pc, ok := auth.ProjectFromContext(r.Context())
		if !ok {
			http.Error(w, `{"error":"missing project context"}`, http.StatusUnauthorized)
			return
		}

		config := tenant.ParseAuthConfig(pc.AuthConfig)

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
			Email string `json:"email"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, map[string]string{"error": "invalid request body"}, http.StatusBadRequest)
			return
		}

		// Rate limit: 3 per email per 15 min.
		email := strings.ToLower(strings.TrimSpace(req.Email))
		if email != "" && ratelimit.CheckAuthRate(rl, w, r.Context(), "forgot_password", email, ratelimit.ForgotPasswordLimit, ratelimit.ForgotPasswordWindow) {
			return
		}

		// Load project name for email template.
		var projectName string
		_ = svc.pool.QueryRow(r.Context(), `SELECT name FROM projects WHERE id = $1`, pc.ProjectID).Scan(&projectName)

		_ = svc.ForgotPassword(r.Context(), pc.SchemaName, pc.ProjectID, projectName, req.Email)

		// Always return 200 to prevent email enumeration.
		writeJSON(w, map[string]string{"status": "ok"}, http.StatusOK)
	}
}

// HandleResetPassword returns an HTTP handler for POST /v1/auth/reset-password.
func HandleResetPassword(svc *AuthService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pc, ok := auth.ProjectFromContext(r.Context())
		if !ok {
			http.Error(w, `{"error":"missing project context"}`, http.StatusUnauthorized)
			return
		}

		config := tenant.ParseAuthConfig(pc.AuthConfig)

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
func HandleVerifyEmail(svc *AuthService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pc, ok := auth.ProjectFromContext(r.Context())
		if !ok {
			http.Error(w, `{"error":"missing project context"}`, http.StatusUnauthorized)
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
			Email string `json:"email"`
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

		err := svc.RequestMagicLink(r.Context(), pc.SchemaName, pc.ProjectID, projectName, config, req.Email)
		if err != nil {
			slog.Warn("request-magic-link failed", "error", err, "project_id", pc.ProjectID)
		}

		// Always return 200 to prevent email enumeration.
		writeJSON(w, map[string]string{"status": "ok"}, http.StatusOK)
	}
}

// HandleSignInWithMagicLink returns an HTTP handler for POST /v1/auth/signin-magic-link.
func HandleSignInWithMagicLink(svc *AuthService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pc, ok := auth.ProjectFromContext(r.Context())
		if !ok {
			http.Error(w, `{"error":"missing project context"}`, http.StatusUnauthorized)
			return
		}

		config := tenant.ParseAuthConfig(pc.AuthConfig)

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
			Email string `json:"email"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, map[string]string{"error": "invalid request body"}, http.StatusBadRequest)
			return
		}

		// Rate limit: 1 per email per 5 min.
		email := strings.ToLower(strings.TrimSpace(req.Email))
		if email != "" && ratelimit.CheckAuthRate(rl, w, r.Context(), "resend_verify", email, ratelimit.ResendVerifyLimit, ratelimit.ResendVerifyWindow) {
			return
		}

		var projectName string
		_ = svc.pool.QueryRow(r.Context(), `SELECT name FROM projects WHERE id = $1`, pc.ProjectID).Scan(&projectName)

		_ = svc.ResendVerification(r.Context(), pc.SchemaName, pc.ProjectID, projectName, req.Email)

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

		// Generate state token for CSRF protection.
		state, err := generateRandomHex(16)
		if err != nil {
			slog.Error("oauth: failed to generate state", "error", err)
			writeJSON(w, map[string]string{"error": "internal server error"}, http.StatusInternalServerError)
			return
		}

		// Encode the client redirect URL and state into the OAuth callback redirect_uri.
		// The callback URL is on the Eurobase gateway itself.
		callbackURL := fmt.Sprintf("%s://%s/v1/auth/oauth/%s/callback", schemeFromRequest(r), r.Host, providerName)

		// Store client redirect and state in the callback URL as query params.
		// We encode the client redirect in the state to avoid server-side session storage.
		encodedState := state + ":" + clientRedirectURL

		provider, err := oauth.Get(providerName)
		if err != nil {
			writeJSON(w, map[string]string{"error": err.Error()}, http.StatusBadRequest)
			return
		}

		authURL := provider.AuthURL(oauth.AuthURLConfig{
			ClientID:    providerConfig.ClientID,
			RedirectURL: callbackURL,
			State:       encodedState,
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
		var code, encodedState string
		if r.Method == http.MethodPost {
			if err := r.ParseForm(); err != nil {
				writeJSON(w, map[string]string{"error": "invalid form data"}, http.StatusBadRequest)
				return
			}
			code = r.FormValue("code")
			encodedState = r.FormValue("state")
		} else {
			code = r.URL.Query().Get("code")
			encodedState = r.URL.Query().Get("state")
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

		// Extract the client redirect URL from the state.
		var clientRedirectURL string
		if idx := strings.Index(encodedState, ":"); idx > 0 {
			clientRedirectURL = encodedState[idx+1:]
		}
		if clientRedirectURL == "" {
			writeJSON(w, map[string]string{"error": "invalid state parameter"}, http.StatusBadRequest)
			return
		}

		// Validate redirect_url against allowed list.
		if !config.IsRedirectURLAllowed(clientRedirectURL) {
			writeJSON(w, map[string]string{"error": "redirect_url is not in the allowed redirect URLs"}, http.StatusBadRequest)
			return
		}

		// Build the callback URL that was used for the token exchange.
		callbackURL := fmt.Sprintf("%s://%s/v1/auth/oauth/%s/callback", schemeFromRequest(r), r.Host, providerName)

		resp, err := svc.SignInWithOAuth(r.Context(), pc.SchemaName, pc.JWTSecret, pc.ProjectID, config, providerName, code, callbackURL)
		if err != nil {
			slog.Warn("oauth signin failed", "error", err, "provider", providerName, "project_id", pc.ProjectID)
			// Redirect to client with error.
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

		if err := svc.SendPhoneOTP(r.Context(), pc.SchemaName, req.Phone); err != nil {
			slog.Warn("send phone otp failed", "error", err)
			writeJSON(w, map[string]string{"error": err.Error()}, http.StatusBadRequest)
			return
		}

		writeJSON(w, map[string]string{"status": "otp_sent"}, http.StatusOK)
	}
}

// HandleVerifyPhoneOTP returns an HTTP handler for POST /v1/auth/phone/verify.
func HandleVerifyPhoneOTP(svc *AuthService) http.HandlerFunc {
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
