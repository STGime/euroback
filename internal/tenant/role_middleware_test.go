package tenant

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/eurobase/euroback/internal/auth"
)

// Closes #50. The middleware gating is exercised in unit tests here;
// the per-route mapping (which routes get which minimum) is a
// table-driven test in internal/gateway/router_role_gate_test.go that
// runs against the real router.

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func runWithRole(t *testing.T, role string, withClaims bool, min string) int {
	t.Helper()
	ctx := context.Background()
	if withClaims {
		ctx = auth.ContextWithClaims(ctx, &auth.Claims{Subject: "user-1", Email: "u@example.com"})
	}
	if role != "" {
		ctx = WithRole(ctx, role)
	}
	r := httptest.NewRequest("GET", "/x", nil).WithContext(ctx)
	rr := httptest.NewRecorder()
	RequireMinRole(min)(okHandler()).ServeHTTP(rr, r)
	return rr.Code
}

func TestRequireMinRole_AcceptsAtLevel(t *testing.T) {
	for _, role := range []string{"viewer", "developer", "admin", "owner"} {
		if got := runWithRole(t, role, true, "viewer"); got != 200 {
			t.Errorf("min=viewer role=%s: got %d, want 200", role, got)
		}
	}
}

func TestRequireMinRole_AcceptsAboveLevel(t *testing.T) {
	for _, role := range []string{"developer", "admin", "owner"} {
		if got := runWithRole(t, role, true, "developer"); got != 200 {
			t.Errorf("min=developer role=%s: got %d, want 200", role, got)
		}
	}
	for _, role := range []string{"admin", "owner"} {
		if got := runWithRole(t, role, true, "admin"); got != 200 {
			t.Errorf("min=admin role=%s: got %d, want 200", role, got)
		}
	}
	if got := runWithRole(t, "owner", true, "owner"); got != 200 {
		t.Errorf("min=owner role=owner: got %d, want 200", got)
	}
}

func TestRequireMinRole_RejectsBelowLevel(t *testing.T) {
	cases := []struct{ role, min string }{
		{"viewer", "developer"},
		{"viewer", "admin"},
		{"viewer", "owner"},
		{"developer", "admin"},
		{"developer", "owner"},
		{"admin", "owner"},
	}
	for _, c := range cases {
		if got := runWithRole(t, c.role, true, c.min); got != 403 {
			t.Errorf("min=%s role=%s: got %d, want 403", c.min, c.role, got)
		}
	}
}

func TestRequireMinRole_RejectsEmptyRoleWithClaims(t *testing.T) {
	// Production path: the user has authenticated (claims present) but
	// somehow has no role (would happen if projectMembershipMiddleware
	// was bypassed by a wiring mistake). Fail closed at 403.
	if got := runWithRole(t, "", true, "viewer"); got != 403 {
		t.Errorf("empty role with claims: got %d, want 403", got)
	}
}

func TestRequireMinRole_AllowsEmptyRoleWithoutClaims(t *testing.T) {
	// Dev-mode path: projectMembershipMiddleware skipped ResolveRole, no
	// claims on context either. Allow through so local curl/Postman
	// continues to work; production cannot reach this branch because
	// platformAuth.Handler always sets claims first.
	if got := runWithRole(t, "", false, "owner"); got != 200 {
		t.Errorf("empty role no claims (dev mode): got %d, want 200", got)
	}
}

func TestRequireMinRole_RejectsUnknownRoleString(t *testing.T) {
	// roleLevel returns 0 for unknown keys, so any min ≥ 1 (i.e., any
	// real minimum) rejects an unknown role.
	if got := runWithRole(t, "superuser", true, "viewer"); got != 403 {
		t.Errorf("unknown role: got %d, want 403", got)
	}
}

func TestWithRole_RoundTrips(t *testing.T) {
	ctx := WithRole(context.Background(), "admin")
	if RoleFromContext(ctx) != "admin" {
		t.Errorf("RoleFromContext after WithRole: got %q, want admin", RoleFromContext(ctx))
	}
	// Empty input is a no-op so we don't pollute context with the empty string.
	if RoleFromContext(WithRole(context.Background(), "")) != "" {
		t.Error("WithRole(\"\") should not set anything")
	}
}
