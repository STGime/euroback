package gateway

import (
	"context"
	"testing"

	"github.com/eurobase/euroback/internal/query"
	"github.com/eurobase/euroback/internal/tenantconn"
)

// With PLATFORM_DDL_LOGIN unset, cmd/gateway has a nil *tenantconn.DDLRunner.
// Wrapped straight into NewRouter's query.TenantLoginRunner parameter it was a
// non-nil interface, so the router attached it and every console SQL / Table
// Editor request panicked on the nil runner (500). PlatformDDLLogin must hand
// NewRouter a real nil so the router keeps the migrator path.
func TestPlatformDDLLogin_NilRunnerIsNilInterface(t *testing.T) {
	var runner *tenantconn.DDLRunner
	if got := PlatformDDLLogin(runner); got != nil {
		t.Fatalf("PlatformDDLLogin(nil runner) = %#v, want a nil interface", got)
	}
}

// Full-wiring guard (regression for the #777 console-500): with a nil runner,
// attachDDLLogin + ContextWithDDLLogin must leave the context with a nil
// _ddl login, so runDDL and the engine keep the migrator path.
func TestAttachDDLLogin_NilRunnerKeepsMigratorPath(t *testing.T) {
	var runner *tenantconn.DDLRunner
	ddlLogin := PlatformDDLLogin(runner) // nil interface
	// Mirror NewRouter: only wrap when non-nil (here it's nil → stays nil).
	if ddlLogin != nil {
		t.Fatal("PlatformDDLLogin(nil) should be a nil interface")
	}
	// The middleware with a nil runner is a no-op on the context.
	ctx := query.ContextWithDDLLogin(context.Background(), ddlLogin)
	if query.DDLLoginFromContext(ctx) != nil {
		t.Fatal("nil runner must not land a _ddl login on the context")
	}
}
