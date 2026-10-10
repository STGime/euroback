package gateway

import (
	"testing"

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
