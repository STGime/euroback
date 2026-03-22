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

func isUserError(err error) bool {
	msg := err.Error()
	return msg == "email is required" ||
		msg == "password must be at least 8 characters" ||
		msg == "email already registered"
}

func writeJSONError(w http.ResponseWriter, msg string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
