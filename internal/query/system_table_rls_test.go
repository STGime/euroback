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
			if !errors.Is(err, ErrSystemTable) || !IsRefused(err) {
				t.Errorf("%s on %s: got %v, want a refused ErrSystemTable", name, table, err)
			}
			if got := systemTableStatus(err, http.StatusBadRequest); got != http.StatusForbidden {
				t.Errorf("%s on %s: status %d, want 403", name, table, got)
			}
		}

		r := chi.NewRouter()
		r.Post("/p/{id}/tables/{table}/rls", handleToggleRLS(nil))
		r.Post("/p/{id}/tables/{table}/policies/preset", handleApplyPreset(nil))
		r.Post("/p/{id}/tables/{table}/policies", handleCreatePolicy(nil))
		r.Delete("/p/{id}/tables/{table}/policies/{policy}", handleDropPolicy(nil))
		for _, req := range []struct{ method, path, body string }{
			{http.MethodPost, "/rls", `{"enabled":false}`},
			{http.MethodPost, "/policies/preset", `{"preset":"full_access"}`},
			{http.MethodPost, "/policies", `{"name":"p","command":"ALL","using":"true"}`},
			{http.MethodDelete, "/policies/p", ``},
		} {
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(req.method, "/p/x/tables/"+table+req.path, strings.NewReader(req.body)))
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s %s on %s: status %d, want 403", req.method, req.path, table, rec.Code)
			}
		}

		// Creating, dropping or renaming one is refused too (on Team,
		// platform SQL runs as the database owner).
		if !reservedTableNames[table] {
			t.Errorf("%s is missing from reservedTableNames", table)
		}
		cols := []ColumnDefinition{{Name: "id", Type: "uuid", IsPrimaryKey: true}}
		for name, err := range map[string]error{
			"create":    CreateTable(ctx, nil, "tenant_x", table, cols),
			"drop":      DropTable(ctx, nil, "tenant_x", table),
			"rename":    RenameTable(ctx, nil, "tenant_x", table, "x"),
			"rename to": RenameTable(ctx, nil, "tenant_x", "todos", table),
		} {
			if !errors.Is(err, ErrSystemTable) || !IsRefused(err) {
				t.Errorf("%s %s: got %v, want a refused ErrSystemTable", name, table, err)
			}
		}
	}

	if err := refuseSystemTableRLS("todos"); err != nil {
		t.Errorf("application table refused: %v", err)
	}
}
