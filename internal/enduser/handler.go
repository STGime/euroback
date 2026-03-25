package enduser

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/tenant"
)

// HandleSignUp returns an HTTP handler for POST /v1/auth/signup.
func HandleSignUp(svc *AuthService) http.HandlerFunc {
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
func HandleSignIn(svc *AuthService) http.HandlerFunc {
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

		resp, err := svc.SignIn(r.Context(), pc.SchemaName, pc.JWTSecret, pc.ProjectID, config, req)
		if err != nil {
			slog.Warn("end-user signin failed", "error", err, "project_id", pc.ProjectID)
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

func writeJSON(w http.ResponseWriter, v interface{}, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
