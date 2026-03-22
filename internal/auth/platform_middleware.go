package auth

import (
	"log/slog"
	"net/http"
	"strings"
)

// PlatformAuthMiddleware validates platform JWTs on protected routes.
type PlatformAuthMiddleware struct {
	svc *PlatformAuthService
}

// NewPlatformAuthMiddleware creates a new middleware that validates platform JWTs.
func NewPlatformAuthMiddleware(svc *PlatformAuthService) *PlatformAuthMiddleware {
	return &PlatformAuthMiddleware{svc: svc}
}

// Handler is the chi-compatible middleware func.
func (m *PlatformAuthMiddleware) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			http.Error(w, `{"error":"missing authorization header"}`, http.StatusUnauthorized)
			return
		}

		tokenStr := strings.TrimPrefix(authHeader, "Bearer ")
		if tokenStr == authHeader {
			http.Error(w, `{"error":"malformed authorization header"}`, http.StatusUnauthorized)
			return
		}

		claims, err := m.svc.ValidatePlatformJWT(tokenStr)
		if err != nil {
			slog.Warn("invalid platform JWT", "error", err, "path", r.URL.Path)
			http.Error(w, `{"error":"invalid or expired token"}`, http.StatusUnauthorized)
			return
		}

		ctx := ContextWithClaims(r.Context(), claims)
		slog.Debug("platform auth OK", "sub", claims.Subject, "email", claims.Email, "path", r.URL.Path)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ValidateToken parses a raw JWT string and returns the subject claim.
// Used by WebSocket handler where token comes as a query parameter.
func (m *PlatformAuthMiddleware) ValidateToken(tokenStr string) (string, error) {
	claims, err := m.svc.ValidatePlatformJWT(tokenStr)
	if err != nil {
		return "", err
	}
	return claims.Subject, nil
}
