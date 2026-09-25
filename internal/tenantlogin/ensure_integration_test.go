package tenantlogin

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestEnsureOneIntegration runs against a database built by
// scripts/db/apply-migrations.sh with at least one provisioned tenant:
//
//	TENANTLOGIN_TEST_DEV_URL=postgres://eurobase_developer:localdev@localhost:5463/eurobase?sslmode=disable \
//	TENANTLOGIN_TEST_SCHEMA=tenant_… go test ./internal/tenantlogin/ -run Integration
//
// It checks that the tenant can log in with the derived password, cannot
// raise its connection limit, and that role defaults it sets on itself
// are cleared by the next pass.
func TestEnsureOneIntegration(t *testing.T) {
	devURL, schema := os.Getenv("TENANTLOGIN_TEST_DEV_URL"), os.Getenv("TENANTLOGIN_TEST_SCHEMA")
	if devURL == "" || schema == "" {
		t.Skip("TENANTLOGIN_TEST_DEV_URL / TENANTLOGIN_TEST_SCHEMA not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, devURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	secret := []byte(strings.Repeat("k", 32))
	e, err := NewEnsurer(pool, pool.Config().ConnConfig.Database, secret)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.EnsureOne(ctx, schema); err != nil {
		t.Fatalf("EnsureOne: %v", err)
	}

	role := FuncRole(schema)
	cfg, _ := pgx.ParseConfig(devURL)
	cfg.User, cfg.Password = role, FuncPassword(secret, schema)
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("login as %s: %v", role, err)
	}
	defer conn.Close(ctx)

	if _, err := conn.Exec(ctx, "ALTER ROLE CURRENT_USER CONNECTION LIMIT -1"); err == nil {
		t.Fatal("tenant raised its own connection limit")
	}
	if _, err := conn.Exec(ctx, "ALTER ROLE CURRENT_USER SET work_mem = '1GB'"); err != nil {
		t.Fatalf("self SET (expected allowed by Postgres): %v", err)
	}

	if err := e.EnsureOne(ctx, schema); err != nil {
		t.Fatalf("second EnsureOne: %v", err)
	}
	var limit int
	var settings []string
	if err := pool.QueryRow(ctx,
		`SELECT r.rolconnlimit, COALESCE(s.setconfig, '{}')
		   FROM pg_roles r LEFT JOIN pg_db_role_setting s ON s.setrole = r.oid AND s.setdatabase = 0
		  WHERE r.rolname = $1`, role).Scan(&limit, &settings); err != nil {
		t.Fatal(err)
	}
	if limit != FuncConnLimit {
		t.Errorf("connection limit = %d, want %d", limit, FuncConnLimit)
	}
	if len(settings) != 0 {
		t.Errorf("role settings not reset: %v", settings)
	}
}
