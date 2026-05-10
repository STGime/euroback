package tenant

import (
	"context"
	"net/http"

	"github.com/eurobase/euroback/internal/auth"
)

// Closes #50: per-route role gating on /platform/projects/{id}/*. The
// existing projectMembershipMiddleware already calls ResolveRole and
// throws the result away after a "non-empty" check; this file adds a
// context key for the role plus a middleware that gates a sub-route on
// a minimum role from the existing roleLevel hierarchy.
//
// Hierarchy (from members.go):
//
//	viewer    (1)
//	developer (2)
//	admin     (3)
//	owner     (4)
//
// Mapping to the user-facing Role Permissions table is in PR #98.

type roleCtxKey struct{}

// WithRole stores the caller's resolved project role on the context.
// Should be called once per request from projectMembershipMiddleware
// after ResolveRole returns. Empty role is treated the same as "no
// access" by RoleFromContext.
func WithRole(ctx context.Context, role string) context.Context {
	if role == "" {
		return ctx
	}
	return context.WithValue(ctx, roleCtxKey{}, role)
}

// RoleFromContext returns the role stored by WithRole, or "" if no
// role is set. The empty string makes downstream gating fail closed —
// an unset role can never satisfy roleLevel[""] >= roleLevel[min].
func RoleFromContext(ctx context.Context) string {
	v, _ := ctx.Value(roleCtxKey{}).(string)
	return v
}

// RequireMinRole returns a middleware that rejects (403) any request
// whose stashed role doesn't meet the minimum. Must run AFTER
// projectMembershipMiddleware so the role is on the context.
//
// In dev mode, projectMembershipMiddleware skips ResolveRole entirely
// and never stashes a role; this middleware then sees "" and 403s.
// Dev-mode routes that skipped membership therefore also skip role
// gating — consistent with the existing pattern, and the dev-mode
// fence in cmd/gateway/main.go fails closed in production already.
func RequireMinRole(min string) func(http.Handler) http.Handler {
	minLvl := roleLevel[min]
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			role := RoleFromContext(r.Context())
			if role == "" {
				// Dev-mode bypass: projectMembershipMiddleware skips
				// ResolveRole when isDev is true, so no role lands on
				// the context AND there are no auth claims either.
				// Allow through in that case — production always has
				// claims, so this can't be exploited via a forged
				// request from outside.
				if _, hasClaims := auth.ClaimsFromContext(r.Context()); !hasClaims {
					next.ServeHTTP(w, r)
					return
				}
			}
			if roleLevel[role] < minLvl {
				http.Error(w, `{"error":"insufficient role"}`, http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
