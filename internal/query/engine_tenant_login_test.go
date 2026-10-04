package query

import (
	"context"
	"errors"
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
