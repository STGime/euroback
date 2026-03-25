package auth

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

type signUpRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type signInRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// HandlePlatformSignUp returns an HTTP handler for POST /platform/auth/signup.
func HandlePlatformSignUp(svc *PlatformAuthService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req signUpRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}

		resp, err := svc.SignUp(r.Context(), req.Email, req.Password)
		if err != nil {
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
func HandlePlatformSignIn(svc *PlatformAuthService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req signInRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}

		resp, err := svc.SignIn(r.Context(), req.Email, req.Password)
		if err != nil {
			slog.Warn("platform signin failed", "error", err, "email", req.Email)
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
func HandlePlatformForgotPassword(svc *PlatformAuthService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Email string `json:"email"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, "invalid request body", http.StatusBadRequest)
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

func isUserError(err error) bool {
	msg := err.Error()
	return msg == "email is required" ||
		msg == "password must be at least 8 characters" ||
		msg == "email already registered" ||
		msg == "display name is required" ||
		msg == "display name must be at most 100 characters" ||
		msg == "new password must be at least 8 characters" ||
		msg == "current password is incorrect" ||
		msg == "delete all projects before deleting your account" ||
		msg == "invalid or expired token" ||
		msg == "email service not configured"
}

func writeJSONError(w http.ResponseWriter, msg string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
