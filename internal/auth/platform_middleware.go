package auth

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
)

// PlatformAuthMiddleware validates platform JWTs on protected routes.
// It also accepts Personal Access Tokens (eb_pat_…) when a PATService is
// configured; PATs route to a DB lookup instead of JWT validation and
// always resolve to non-superadmin claims regardless of the underlying
// user's flag.
type PlatformAuthMiddleware struct {
	svc *PlatformAuthService
	pat *PATService
}

// NewPlatformAuthMiddleware creates a new middleware that validates platform JWTs.
func NewPlatformAuthMiddleware(svc *PlatformAuthService) *PlatformAuthMiddleware {
	return &PlatformAuthMiddleware{svc: svc}
}

// WithPATService enables PAT acceptance on this middleware. Returns the
// middleware for chaining.
func (m *PlatformAuthMiddleware) WithPATService(pat *PATService) *PlatformAuthMiddleware {
	m.pat = pat
	return m
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

		claims, err := m.resolve(r, tokenStr)
		if err != nil {
			slog.Warn("platform auth failed", "error", err, "path", r.URL.Path)
			http.Error(w, `{"error":"invalid or expired token"}`, http.StatusUnauthorized)
			return
		}

		if claims.PATProjectID != "" && !ScopedTokenAllowed(r.Method, r.URL.Path) {
			slog.Info("project-scoped token refused outside its project", "token_id", claims.PATID, "path", r.URL.Path)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"this token is for one project; this request is outside it","code":"pat_out_of_scope"}`))
			return
		}

		ctx := ContextWithClaims(r.Context(), claims)
		slog.Debug("platform auth OK", "sub", claims.Subject, "email", claims.Email, "path", r.URL.Path)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ScopedTokenAllowed is the deny-by-default allowlist for a project-scoped
// token (#702): the project routes (/platform/projects/{id}/… — which
// project and how much is decided by tenant.CallerProjectRole: another
// project is a 404, the role is capped), the project list (filtered to
// the token's project), and the profile (the MCP server validates tokens
// with it). Everything else — account, orgs, invitations, billing,
// support, config, project create / settings / move / delete — is refused.
func ScopedTokenAllowed(method, path string) bool {
	switch {
	case strings.HasPrefix(path, "/platform/projects/"):
		return true
	case (path == "/v1/tenants" || path == "/v1/tenants/") && method == http.MethodGet:
		return true
	case path == "/platform/auth/account/profile" && method == http.MethodGet:
		return true
	}
	return false
}

// resolve picks PAT vs JWT based on prefix. PATs short-circuit before JWT
// parsing because their format isn't a JWT and would fail the signature
// check with a confusing error.
func (m *PlatformAuthMiddleware) resolve(r *http.Request, tokenStr string) (*Claims, error) {
	if strings.HasPrefix(tokenStr, PATPrefix) {
		if m.pat == nil {
			return nil, errors.New("pat received but PATService not configured")
		}
		return m.pat.Validate(r.Context(), tokenStr)
	}
	return m.svc.ValidatePlatformJWT(tokenStr)
}

// ValidatePlatformJWT parses the token and returns the full Claims —
// used by callers that need LoginVia / SsoOrgID for
// organizations.sso_required enforcement (realtime authorize).
// ValidateToken returns only the subject and can't carry provenance.
func (m *PlatformAuthMiddleware) ValidatePlatformJWT(tokenStr string) (*Claims, error) {
	return m.svc.ValidatePlatformJWT(tokenStr)
}

// ValidateToken parses a raw JWT string and returns the subject claim.
// Used by WebSocket handler where token comes as a query parameter.
// Note: WS path stays JWT-only; PATs are not intended for streaming auth.
func (m *PlatformAuthMiddleware) ValidateToken(tokenStr string) (string, error) {
	claims, err := m.svc.ValidatePlatformJWT(tokenStr)
	if err != nil {
		return "", err
	}
	return claims.Subject, nil
}
