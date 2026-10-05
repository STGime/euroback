package query

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5"
)

// fakeLogin records how the engine invokes a TenantLoginRunner. It never
// touches the (nil) pool, so these tests need no database.
type fakeLogin struct {
	calls    int
	schema   string
	readOnly bool
	ret      error
}

func (f *fakeLogin) Run(ctx context.Context, schema string, readOnly bool, setup, fn func(context.Context, pgx.Tx) error) error {
	f.calls++
	f.schema = schema
	f.readOnly = readOnly
	return f.ret
}

// The SDK /sql path (SDKPath=true) must run on the per-project login when
// one is attached — never the shared pool. The engine's pool is nil here,
// so if it fell through to the pool the test would panic.
func TestExecuteSQLWithOpts_RoutesToTenantLogin(t *testing.T) {
	fl := &fakeLogin{}
	e := NewQueryEngine(nil).WithTenantLogin(fl)

	if _, _, err := e.ExecuteSQLWithOpts(context.Background(), "tenant_abc", "SELECT 1", 10, ExecOptions{ReadOnly: true, SDKPath: true}); err != nil {
		t.Fatalf("ExecuteSQLWithOpts: %v", err)
	}
	if fl.calls != 1 {
		t.Fatalf("login runner called %d times, want 1", fl.calls)
	}
	if fl.schema != "tenant_abc" {
		t.Fatalf("login runner schema = %q, want tenant_abc", fl.schema)
	}
	if !fl.readOnly {
		t.Fatalf("SDK /sql must open a read-only tx on the login")
	}
}

// A login that can't be opened surfaces as ErrTenantLoginUnavailable (→ 503
// in the handler), never a fall-through to the pool.
func TestExecuteSQLWithOpts_LoginUnavailable(t *testing.T) {
	fl := &fakeLogin{ret: ErrTenantLoginUnavailable}
	e := NewQueryEngine(nil).WithTenantLogin(fl)

	_, _, err := e.ExecuteSQLWithOpts(context.Background(), "tenant_abc", "SELECT 1", 10, ExecOptions{ReadOnly: true, SDKPath: true})
	if !errors.Is(err, ErrTenantLoginUnavailable) {
		t.Fatalf("err = %v, want ErrTenantLoginUnavailable", err)
	}
}

// The data API (typed REST / RPC) runs through WithTenantTx, which must
// route to the per-project login for SDK traffic (step 5). The nil pool
// would panic if it fell through, so reaching the fake proves routing.
func TestWithTenantTx_RoutesToTenantLogin(t *testing.T) {
	fl := &fakeLogin{}
	e := NewQueryEngine(nil).WithTenantLogin(fl)

	if err := e.WithTenantTx(context.Background(), "tenant_abc", func(tx pgx.Tx) error { return nil }); err != nil {
		t.Fatalf("WithTenantTx: %v", err)
	}
	if fl.calls != 1 || fl.schema != "tenant_abc" {
		t.Fatalf("login runner calls=%d schema=%q, want 1 / tenant_abc", fl.calls, fl.schema)
	}
	if fl.readOnly {
		t.Fatalf("data-API tx must be read-write")
	}
}

// Console/platform traffic (DeveloperRole) must NOT use the tenant login —
// it runs as migrator for DDL, and a dedicated instance has no migrator role.
// With a nil pool the pool path fails/panics, proving the login was skipped.
func TestWithTenantTx_DeveloperRoleSkipsLogin(t *testing.T) {
	fl := &fakeLogin{}
	e := NewQueryEngine(nil).WithTenantLogin(fl)

	defer func() {
		_ = recover()
		if fl.calls != 0 {
			t.Fatalf("developer-role path used the tenant login (%d calls)", fl.calls)
		}
	}()
	_ = e.WithTenantTx(WithDeveloperRole(context.Background()), "tenant_abc", func(tx pgx.Tx) error { return nil })
	if fl.calls != 0 {
		t.Fatalf("developer-role path used the tenant login (%d calls)", fl.calls)
	}
}

// The platform (console) SQL path (SDKPath=false) must NOT use the tenant
// login — it runs developer-authored SQL as the developer pool / migrator.
// With a nil pool the attempt to acquire a connection fails, proving the
// login was not consulted (fl.calls stays 0).
func TestExecuteSQLWithOpts_PlatformPathSkipsLogin(t *testing.T) {
	fl := &fakeLogin{}
	e := NewQueryEngine(nil).WithTenantLogin(fl)

	defer func() {
		_ = recover() // a nil pool may panic on Acquire; either way the login must be untouched
		if fl.calls != 0 {
			t.Fatalf("platform path used the tenant login (%d calls)", fl.calls)
		}
	}()
	_, _, _ = e.ExecuteSQLWithOpts(context.Background(), "tenant_abc", "SELECT 1", 10, ExecOptions{ReadOnly: false, SDKPath: false})
	if fl.calls != 0 {
		t.Fatalf("platform path used the tenant login (%d calls)", fl.calls)
	}
}

// handleQueryError must map ErrTenantLoginUnavailable to 503 for the typed-
// REST data API (parity with /v1/db/sql and RPC), not a generic 500.
func TestHandleQueryError_LoginUnavailable503(t *testing.T) {
	w := httptest.NewRecorder()
	if !handleQueryError(w, fmt.Errorf("wrap: %w", ErrTenantLoginUnavailable)) {
		t.Fatal("handleQueryError did not handle ErrTenantLoginUnavailable")
	}
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}
