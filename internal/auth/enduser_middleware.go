package auth

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/eurobase/euroback/internal/query"
	"github.com/golang-jwt/jwt/v5"
)

type endUserClaimsKey struct{}

// EndUserClaims holds the authenticated end-user's identity from a JWT.
type EndUserClaims struct {
	UserID    string
	Email     string
	ProjectID string
}

// ContextWithEndUserClaims stores end-user claims in the context.
func ContextWithEndUserClaims(ctx context.Context, c *EndUserClaims) context.Context {
	return context.WithValue(ctx, endUserClaimsKey{}, c)
}

// EndUserClaimsFromContext retrieves end-user claims from the context.
func EndUserClaimsFromContext(ctx context.Context) (*EndUserClaims, bool) {
	c, ok := ctx.Value(endUserClaimsKey{}).(*EndUserClaims)
	return c, ok
}

// EndUserMiddleware validates end-user JWTs using the project's JWT secret.
// It is optional: if no Authorization header is present, the request proceeds
// without end-user context (anonymous access).
type EndUserMiddleware struct{}

// NewEndUserMiddleware creates a new end-user auth middleware.
func NewEndUserMiddleware() *EndUserMiddleware {
	return &EndUserMiddleware{}
}

// Handler is the chi-compatible middleware func.
// It requires ProjectContext to already be set by APIKeyMiddleware.
func (m *EndUserMiddleware) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			// Anonymous access — proceed without end-user context.
			next.ServeHTTP(w, r)
			return
		}

		tokenStr := strings.TrimPrefix(authHeader, "Bearer ")
		if tokenStr == authHeader {
			http.Error(w, `{"error":"malformed authorization header"}`, http.StatusUnauthorized)
			return
		}

		pc, ok := ProjectFromContext(r.Context())
		if !ok {
			http.Error(w, `{"error":"missing project context"}`, http.StatusUnauthorized)
			return
		}

		claims, err := validateEndUserJWT(tokenStr, pc.JWTSecret)
		if err != nil {
			slog.Warn("invalid end-user JWT", "error", err, "project_id", pc.ProjectID)
			http.Error(w, `{"error":"invalid or expired token"}`, http.StatusUnauthorized)
			return
		}

		ctx := ContextWithEndUserClaims(r.Context(), claims)
		// Also store in query context for RLS.
		ctx = query.ContextWithEndUserID(ctx, claims.UserID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func validateEndUserJWT(tokenStr, secret string) (*EndUserClaims, error) {
	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(secret), nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil || !token.Valid {
		return nil, fmt.Errorf("invalid token: %w", err)
	}

	mapClaims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, fmt.Errorf("invalid claims type")
	}

	tokenType, _ := mapClaims["type"].(string)
	if tokenType != "enduser" {
		return nil, fmt.Errorf("not an end-user token")
	}

	sub, _ := mapClaims.GetSubject()
	email, _ := mapClaims["email"].(string)
	projectID, _ := mapClaims["project_id"].(string)

	if sub == "" {
		return nil, fmt.Errorf("token missing subject")
	}

	return &EndUserClaims{
		UserID:    sub,
		Email:     email,
		ProjectID: projectID,
	}, nil
}
