package query

import (
	"testing"

	"github.com/eurobase/euroback/internal/tenantlogin"
)

// Tenant migrations and the persistent DDL login keeper must derive the
// same password for a tenant's `_ddl` role, or one would lock out the other.
func TestDDLPasswordMatchesTenantlogin(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	e := &MigrationExecutor{passwordSecret: secret}
	if got, want := e.ddlRolePassword("tenant_abc"), tenantlogin.DDLPassword(secret, "tenant_abc"); got != want {
		t.Fatalf("migration executor %s, tenantlogin %s", got, want)
	}
}
