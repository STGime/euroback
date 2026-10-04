package tenantconn

import (
	"context"
	"errors"
	"testing"

	"github.com/eurobase/euroback/internal/tenantlogin"
	"github.com/jackc/pgx/v5"
)

func baseConfig(t *testing.T) *pgx.ConnConfig {
	t.Helper()
	cfg, err := pgx.ParseConfig("postgres://eurobase_gateway:pw@shared.internal:5432/eurobase?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestConfigured(t *testing.T) {
	if (&Resolver{}).Configured() {
		t.Error("empty resolver is Configured")
	}
	var nilR *Resolver
	if nilR.Configured() {
		t.Error("nil resolver is Configured")
	}
	if !NewResolver(baseConfig(t), []byte("0123456789abcdef0123456789abcdef"), nil, "app").Configured() {
		t.Error("fully configured resolver reports not configured")
	}
}

// Shared-cluster config logs in as <schema>_func with the derived password,
// keeps the base host/db, and tags the connection with the caller's name.
func TestConnConfig_Shared(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	r := NewResolver(baseConfig(t), secret, nil, "eurobase-test")
	cfg, err := r.ConnConfig(context.Background(), "proj-1", "tenant_abc")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.User, tenantlogin.FuncRole("tenant_abc"); got != want {
		t.Errorf("User = %q, want %q", got, want)
	}
	if got, want := cfg.Password, tenantlogin.FuncPassword(secret, "tenant_abc"); got != want {
		t.Errorf("Password not the derived FuncPassword")
	}
	if cfg.Host != "shared.internal" || cfg.Database != "eurobase" {
		t.Errorf("host/db not taken from base: %s/%s", cfg.Host, cfg.Database)
	}
	if cfg.RuntimeParams["application_name"] != "eurobase-test" {
		t.Errorf("application_name = %q", cfg.RuntimeParams["application_name"])
	}
	// The base config must not be mutated (Copy()).
	if r.base.User != "eurobase_gateway" {
		t.Errorf("base config User was mutated to %q", r.base.User)
	}
}

func TestConnConfig_NotConfigured(t *testing.T) {
	for _, r := range []*Resolver{
		{},
		NewResolver(nil, []byte("0123456789abcdef0123456789abcdef"), nil, "app"),
		NewResolver(baseConfig(t), nil, nil, "app"),
	} {
		if _, err := r.ConnConfig(context.Background(), "p", "s"); !errors.Is(err, ErrNotConfigured) {
			t.Errorf("want ErrNotConfigured, got %v", err)
		}
	}
}
