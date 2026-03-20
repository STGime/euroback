package auth

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
)

// HankoMiddleware validates JWTs issued by Hanko using JWKS discovery.
type HankoMiddleware struct {
	hankoAPIURL string
	jwks        keyfunc.Keyfunc
	mu          sync.Mutex
	initialized bool
}

// NewHankoMiddleware creates a new middleware that validates tokens against
// the given Hanko API URL (e.g. "https://hanko.example.com").
func NewHankoMiddleware(hankoAPIURL string) *HankoMiddleware {
	return &HankoMiddleware{
		hankoAPIURL: strings.TrimRight(hankoAPIURL, "/"),
	}
}

// initJWKS lazily fetches and caches the JWKS from the Hanko API.
func (h *HankoMiddleware) initJWKS(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.initialized {
		return nil
	}

	jwksURL := h.hankoAPIURL + "/.well-known/jwks.json"
	slog.Info("fetching Hanko JWKS", "url", jwksURL)

	jwks, err := keyfunc.NewDefaultCtx(ctx, []string{jwksURL})
	if err != nil {
		return err
	}

	h.jwks = jwks
	h.initialized = true
	slog.Info("Hanko JWKS loaded successfully")
	return nil
}

// Handler returns an http.Handler middleware that validates the JWT
// from the Authorization header and injects claims into the request context.
func (h *HankoMiddleware) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Initialize JWKS on first request.
		if err := h.initJWKS(r.Context()); err != nil {
			slog.Error("failed to initialize JWKS", "error", err)
			http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
			return
		}

		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			slog.Warn("missing authorization header", "path", r.URL.Path)
			http.Error(w, `{"error":"missing authorization header"}`, http.StatusUnauthorized)
			return
		}

		tokenStr := strings.TrimPrefix(authHeader, "Bearer ")
		if tokenStr == authHeader {
			slog.Warn("malformed authorization header", "path", r.URL.Path)
			http.Error(w, `{"error":"malformed authorization header"}`, http.StatusUnauthorized)
			return
		}

		token, err := jwt.Parse(tokenStr, h.jwks.Keyfunc,
			jwt.WithValidMethods([]string{"RS256", "ES256"}),
		)
		if err != nil || !token.Valid {
			slog.Warn("invalid JWT", "error", err, "path", r.URL.Path)
			http.Error(w, `{"error":"invalid or expired token"}`, http.StatusUnauthorized)
			return
		}

		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			slog.Error("unexpected claims type in JWT")
			http.Error(w, `{"error":"invalid token claims"}`, http.StatusUnauthorized)
			return
		}

		sub, _ := claims.GetSubject()
		email, _ := claims["email"].(string)

		if sub == "" {
			slog.Warn("JWT missing sub claim", "path", r.URL.Path)
			http.Error(w, `{"error":"token missing subject"}`, http.StatusUnauthorized)
			return
		}

		ctx := contextWithClaims(r.Context(), &Claims{
			Subject: sub,
			Email:   email,
		})

		slog.Debug("authenticated request", "sub", sub, "email", email, "path", r.URL.Path)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ValidateToken parses and validates a raw JWT string (without the "Bearer "
// prefix) and returns the subject claim. This is used by the WebSocket handler
// where the token is passed as a query parameter rather than an HTTP header.
func (h *HankoMiddleware) ValidateToken(tokenStr string) (string, error) {
	if err := h.initJWKS(context.Background()); err != nil {
		return "", err
	}

	token, err := jwt.Parse(tokenStr, h.jwks.Keyfunc,
		jwt.WithValidMethods([]string{"RS256", "ES256"}),
	)
	if err != nil || !token.Valid {
		return "", fmt.Errorf("invalid token: %w", err)
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return "", fmt.Errorf("unexpected claims type")
	}

	sub, _ := claims.GetSubject()
	if sub == "" {
		return "", fmt.Errorf("token missing subject claim")
	}

	return sub, nil
}
