package auth

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/eurobase/euroback/internal/clientip"
)

// AuthRateLimiter is a function that checks rate limits and writes a 429 if exceeded.
// Returns true if the request should be blocked. Avoids import cycle with ratelimit package.
type AuthRateLimiter func(w http.ResponseWriter, r *http.Request, action, identifier string) bool

type signUpRequest struct {
	Email    string             `json:"email"`
	Password string             `json:"password"`
	Accepted []AcceptedDocument `json:"accepted_documents"`
}

type signInRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// HandlePlatformSignUp returns an HTTP handler for POST /platform/auth/signup.
func HandlePlatformSignUp(svc *PlatformAuthService, rateFn ...AuthRateLimiter) http.HandlerFunc {
	var check AuthRateLimiter
	if len(rateFn) > 0 {
		check = rateFn[0]
	}
	return func(w http.ResponseWriter, r *http.Request) {
		// Rate limit signups by IP: 5/hour.
		if check != nil && check(w, r, "platform_signup", clientIP(r)) {
			return
		}

		var req signUpRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}

		resp, err := svc.SignUp(r.Context(), req.Email, req.Password, req.Accepted, clientIP(r), r.UserAgent())
		if err != nil {
			if _, ok := err.(*WaitlistError); ok {
				slog.Info("signup blocked by allowlist", "email", req.Email)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				json.NewEncoder(w).Encode(map[string]string{
					"error":   "waitlist",
					"message": "Eurobase is currently in closed beta. You've been added to the waitlist and we'll notify you when your spot opens up.",
				})
				return
			}
			// Stale document version → 400 with the "please refresh"
			// message so the console can re-fetch the current
			// legal_documents set and retry. Typed sentinel so we
			// don't string-match a human-readable error. (#279
			// review high #1.)
			if _, ok := err.(*ErrStaleDocumentVersion); ok {
				slog.Info("signup rejected: stale document version", "email", req.Email, "error", err)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{
					"error":   "stale_document_version",
					"message": err.Error(),
				})
				return
			}
			slog.Warn("platform signup failed", "error", err)
			status := http.StatusInternalServerError
			if isUserError(err) {
				status = http.StatusBadRequest
			}
			writeJSONError(w, err.Error(), status)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(resp)
	}
}

// HandlePlatformSignIn returns an HTTP handler for POST /platform/auth/signin.
func HandlePlatformSignIn(svc *PlatformAuthService, rateFn ...AuthRateLimiter) http.HandlerFunc {
	var check AuthRateLimiter
	if len(rateFn) > 0 {
		check = rateFn[0]
	}
	return func(w http.ResponseWriter, r *http.Request) {
		var req signInRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}

		// Check signin failure rate limit before attempting auth.
		email := strings.ToLower(strings.TrimSpace(req.Email))
		if email != "" && check != nil && check(w, r, "signin_fail", "platform:"+email) {
			return
		}

		resp, err := svc.SignIn(r.Context(), req.Email, req.Password)
		if err != nil {
			// Correct password but unverified email → distinct 403 with a
			// machine code so the console can offer "resend". Credentials
			// were valid, so this isn't counted as a signin failure.
			if errors.Is(err, ErrEmailNotVerified) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				json.NewEncoder(w).Encode(map[string]string{
					"error":   "email_not_verified",
					"message": "Please verify your email before signing in — check your inbox for the confirmation link.",
				})
				return
			}
			slog.Warn("platform signin failed", "error", err, "email", req.Email)
			// Record the failure for rate limiting (call check to increment counter).
			if email != "" && check != nil {
				check(w, r, "signin_fail_record", "platform:"+email)
			}
			status := http.StatusUnauthorized
			if isUserError(err) {
				status = http.StatusBadRequest
			}
			writeJSONError(w, err.Error(), status)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

// HandleGetProfile returns an HTTP handler for GET /platform/auth/account/profile.
//
// When the request is authenticated via a Personal Access Token, the
// is_superadmin field is forced to false so the response reflects the
// token's actual authority rather than the underlying account's flag.
// (PATs never carry superadmin claims at the middleware layer; this
// keeps the profile shape consistent with that.)
func HandleGetProfile(svc *PlatformAuthService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := ClaimsFromContext(r.Context())
		if !ok {
			writeJSONError(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		profile, err := svc.GetProfile(r.Context(), claims.Subject)
		if err != nil {
			slog.Warn("get profile failed", "error", err)
			writeJSONError(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if isPATAuth(r) {
			profile.IsSuperadmin = false
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(profile)
	}
}

// HandleUpdateProfile returns an HTTP handler for PATCH /platform/auth/account/profile.
func HandleUpdateProfile(svc *PlatformAuthService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := ClaimsFromContext(r.Context())
		if !ok {
			writeJSONError(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		var req struct {
			DisplayName string `json:"display_name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, "invalid request body", http.StatusBadRequest)
			return
		}

		if err := svc.UpdateDisplayName(r.Context(), claims.Subject, req.DisplayName); err != nil {
			slog.Warn("update profile failed", "error", err)
			status := http.StatusInternalServerError
			if isUserError(err) {
				status = http.StatusBadRequest
			}
			writeJSONError(w, err.Error(), status)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

// HandleChangePassword returns an HTTP handler for POST /platform/auth/account/change-password.
func HandleChangePassword(svc *PlatformAuthService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := ClaimsFromContext(r.Context())
		if !ok {
			writeJSONError(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		var req struct {
			CurrentPassword string `json:"current_password"`
			NewPassword     string `json:"new_password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, "invalid request body", http.StatusBadRequest)
			return
		}

		if err := svc.ChangePassword(r.Context(), claims.Subject, req.CurrentPassword, req.NewPassword); err != nil {
			slog.Warn("change password failed", "error", err)
			status := http.StatusInternalServerError
			if isUserError(err) {
				status = http.StatusBadRequest
			}
			writeJSONError(w, err.Error(), status)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

// HandleDeleteAccount returns an HTTP handler for POST /platform/auth/account/delete.
func HandleDeleteAccount(svc *PlatformAuthService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := ClaimsFromContext(r.Context())
		if !ok {
			writeJSONError(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		var req struct {
			ConfirmationEmail string `json:"confirmation_email"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, "invalid request body", http.StatusBadRequest)
			return
		}

		if req.ConfirmationEmail != claims.Email {
			writeJSONError(w, "confirmation email does not match", http.StatusBadRequest)
			return
		}

		if err := svc.DeleteAccount(r.Context(), claims.Subject); err != nil {
			slog.Warn("delete account failed", "error", err)
			status := http.StatusInternalServerError
			if isUserError(err) {
				status = http.StatusBadRequest
			}
			writeJSONError(w, err.Error(), status)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}

// HandlePlatformForgotPassword returns an HTTP handler for POST /platform/auth/forgot-password.
func HandlePlatformForgotPassword(svc *PlatformAuthService, rateFn ...AuthRateLimiter) http.HandlerFunc {
	var check AuthRateLimiter
	if len(rateFn) > 0 {
		check = rateFn[0]
	}
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Email string `json:"email"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, "invalid request body", http.StatusBadRequest)
			return
		}

		// Rate limit: 3 per email per 15 min.
		email := strings.ToLower(strings.TrimSpace(req.Email))
		if email != "" && check != nil && check(w, r, "platform_forgot", email) {
			return
		}

		_ = svc.ForgotPassword(r.Context(), req.Email)

		// Always return 200 to prevent email enumeration.
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

// HandlePlatformResetPassword returns an HTTP handler for POST /platform/auth/reset-password.
func HandlePlatformResetPassword(svc *PlatformAuthService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Token    string `json:"token"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, "invalid request body", http.StatusBadRequest)
			return
		}

		if err := svc.ResetPasswordWithToken(r.Context(), req.Token, req.Password); err != nil {
			slog.Warn("platform password reset failed", "error", err)
			status := http.StatusBadRequest
			if isUserError(err) {
				status = http.StatusBadRequest
			}
			writeJSONError(w, err.Error(), status)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

// HandlePlatformVerifyEmail returns an HTTP handler for
// POST /platform/auth/verify-email. Consumes a verification token and,
// on success, returns a session (the link both verifies and signs in).
func HandlePlatformVerifyEmail(svc *PlatformAuthService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.Token) == "" {
			writeJSONError(w, "verification token is required", http.StatusBadRequest)
			return
		}

		resp, err := svc.VerifyEmail(r.Context(), req.Token)
		if err != nil {
			slog.Info("platform email verification failed", "error", err)
			// "invalid or expired token" is user-fixable (request a
			// new link) → 400; anything else is internal → 500.
			status := http.StatusInternalServerError
			if isUserError(err) {
				status = http.StatusBadRequest
			}
			writeJSONError(w, err.Error(), status)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

// HandlePlatformResendVerification returns an HTTP handler for
// POST /platform/auth/resend-verification. Always 200 (no enumeration).
func HandlePlatformResendVerification(svc *PlatformAuthService, rateFn ...AuthRateLimiter) http.HandlerFunc {
	var check AuthRateLimiter
	if len(rateFn) > 0 {
		check = rateFn[0]
	}
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Email string `json:"email"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, "invalid request body", http.StatusBadRequest)
			return
		}

		// Dedicated resend limiter (ResendVerifyLimit/Window) — a
		// verification resend is more abusable as a mail-bomb vector
		// than forgot-password, so it gets its own tighter budget.
		email := strings.ToLower(strings.TrimSpace(req.Email))
		if email != "" && check != nil && check(w, r, "platform_resend_verification", email) {
			return
		}

		_ = svc.ResendVerification(r.Context(), req.Email)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

func isUserError(err error) bool {
	// Password-policy rejections are user-fixable → 400, not 500.
	var weak *ErrWeakPassword
	if errors.As(err, &weak) {
		return true
	}
	msg := err.Error()
	return msg == "email is required" ||
		msg == "email already registered" ||
		msg == "display name is required" ||
		msg == "display name must be at most 100 characters" ||
		msg == "current password is incorrect" ||
		msg == "delete all projects before deleting your account" ||
		msg == "invalid or expired token" ||
		msg == "email service not configured" ||
		msg == "unknown mailing category" ||
		strings.HasPrefix(msg, "you must accept the following to sign up:")
}

func writeJSONError(w http.ResponseWriter, msg string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// clientIP resolves the caller's IP from the TRUSTED side of
// X-Forwarded-For (see internal/clientip). The platform auth routes —
// signup rate limit, the signup IP written to legal_acceptances,
// forgot-password / resend-verification limits — sit behind the same
// ingress as everything else, so the real client is the entry the proxy
// APPENDED, not the leftmost one the client controls. Previously this
// took the leftmost entry unconditionally, so `X-Forwarded-For: 8.8.4.4`
// bypassed the 5/hour signup limit and poisoned the audit IP (security
// report 2026-09-16, secondary finding). Fails closed to the TCP peer
// when the forwarded chain doesn't match the configured hop count.
func clientIP(r *http.Request) string {
	return clientip.FromRequestDefault(r)
}
