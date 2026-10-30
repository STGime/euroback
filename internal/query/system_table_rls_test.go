package query

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// RLS and policy changes on the platform's own tenant tables are refused
// before any database work, on every tier.
func TestSystemTableRLSRefused(t *testing.T) {
	ctx := context.Background()
	for _, table := range tenantSystemTables {
		checks := map[string]error{
			"preset":        ApplyPolicyPreset(ctx, nil, "tenant_x", table, "full_access", ""),
			"create policy": CreateCustomPolicy(ctx, nil, "tenant_x", table, "p", "ALL", "true", ""),
			"drop policy":   DropPolicy(ctx, nil, "tenant_x", table, "user_identities_policy"),
		}
		for name, err := range checks {
			if !errors.Is(err, ErrSystemTableRLS) || !IsRefused(err) {
				t.Errorf("%s on %s: got %v, want a refused ErrSystemTableRLS", name, table, err)
			}
			if got := systemTableStatus(err, http.StatusBadRequest); got != http.StatusForbidden {
				t.Errorf("%s on %s: status %d, want 403", name, table, got)
			}
		}

		r := chi.NewRouter()
		r.Post("/p/{id}/tables/{table}/rls", handleToggleRLS(nil))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/p/x/tables/"+table+"/rls", strings.NewReader(`{"enabled":false}`)))
		if rec.Code != http.StatusForbidden {
			t.Errorf("toggle RLS on %s: status %d, want 403", table, rec.Code)
		}
	}

	if err := refuseSystemTableRLS("todos"); err != nil {
		t.Errorf("application table refused: %v", err)
	}
}
