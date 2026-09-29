package query

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// #702: a platform Viewer (console session or token) gets project data
// only; Developer and up, and the SDK path (no platform role), unchanged.
func TestInternalTableReadGuard(t *testing.T) {
	viewer := WithPlatformRole(context.Background(), "viewer")
	for table := range internalTenantTables {
		if err := checkInternalTableRead(viewer, table); !errors.Is(err, ErrInternalTable) {
			t.Errorf("viewer read of %s: %v, want ErrInternalTable", table, err)
		}
	}
	if err := checkInternalTableRead(viewer, "todos"); err != nil {
		t.Errorf("viewer read of a project table: %v", err)
	}
	for _, ctx := range []context.Context{
		WithPlatformRole(context.Background(), "developer"),
		WithPlatformRole(context.Background(), "admin"),
		WithPlatformRole(context.Background(), "owner"),
		context.Background(), // SDK path: RLS and the column denylist apply instead
	} {
		if err := checkInternalTableRead(ctx, "vault_secrets"); err != nil {
			t.Errorf("role %q refused: %v", PlatformRoleFromContext(ctx), err)
		}
	}
	// Fail closed: an unknown platform role is restricted too.
	if err := checkInternalTableRead(WithPlatformRole(context.Background(), "auditor"), "users"); !errors.Is(err, ErrInternalTable) {
		t.Errorf("unknown role reads users: %v", err)
	}
	if got := PublishableRow("users", map[string]interface{}{"id": 1, "email": "a", "password_hash": "x"}); got["password_hash"] != nil || got["email"] != "a" {
		t.Errorf("PublishableRow users = %v", got)
	}
	if got := PublishableRow("vault_secrets", map[string]interface{}{"name": "n", "secret": "s", "nonce": "n"}); got["secret"] != nil || got["nonce"] != nil {
		t.Errorf("PublishableRow vault_secrets = %v", got)
	}
	got := visibleSchemaTables(viewer, []string{"todos", "users", "vault_secrets", "orders"})
	if len(got) != 2 || got[0] != "todos" || got[1] != "orders" {
		t.Errorf("viewer schema tables = %v", got)
	}
	if got := visibleSchemaTables(context.Background(), []string{"users"}); len(got) != 1 {
		t.Errorf("non-viewer schema tables filtered: %v", got)
	}
}

// The engine refuses before touching the database (nil pool here), and the
// handler maps the refusal to 403 — for rows, aggregates and relations.
func TestInternalTableReadGuard_EngineAndHandler(t *testing.T) {
	e := NewQueryEngine(nil)
	viewer := WithPlatformRole(context.Background(), "viewer")
	if _, _, err := e.SelectRows(viewer, "tenant_x", "users", QueryParams{}); !errors.Is(err, ErrInternalTable) {
		t.Errorf("SelectRows: %v", err)
	}
	if _, err := e.AggregateQuery(viewer, "tenant_x", "vault_secrets", QueryParams{}); !errors.Is(err, ErrInternalTable) {
		t.Errorf("AggregateQuery: %v", err)
	}
	if _, err := e.resolveRelations(viewer, "tenant_x", "todos", []Relation{{Table: "users", Columns: []string{"email"}}}); !errors.Is(err, ErrInternalTable) {
		t.Errorf("resolveRelations: %v", err)
	}
	rec := httptest.NewRecorder()
	if !handleQueryError(rec, checkInternalTableRead(viewer, "users")) || rec.Code != http.StatusForbidden {
		t.Errorf("handler status %d, want 403", rec.Code)
	}
}
