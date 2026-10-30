package query

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestApplyDedicated_DB runs ApplyDedicated against real Postgres. One
// database plays both parts here (the shared cluster's history and the
// "dedicated" owner pool); the Team harness covers the split for real.
//
//	CONVERGE_TEST_ADMIN_URL=postgres://postgres@localhost:5432/eurobase \
//	go test ./internal/query/ -run TestApplyDedicated_DB
func TestApplyDedicated_DB(t *testing.T) {
	adminURL := os.Getenv("CONVERGE_TEST_ADMIN_URL")
	if adminURL == "" {
		t.Skip("CONVERGE_TEST_ADMIN_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	suffix := fmt.Sprintf("%d", os.Getpid())
	schema := "tenant_appded_" + suffix
	var ownerID, projectID string
	if err := pool.QueryRow(ctx, `INSERT INTO platform_users (email) VALUES ($1) RETURNING id`, "appded-"+suffix+"@test.eurobase.local").Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO projects (owner_id, name, slug, schema_name, s3_bucket, region, plan, status)
		 VALUES ($1, 'AppDed', $2, $3, $4, 'fr-par', 'free', 'active') RETURNING id`,
		ownerID, "appded-"+suffix, schema, "b-appded-"+suffix).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM public.tenant_migrations WHERE project_id = $1`, projectID)
		_, _ = pool.Exec(c, `DELETE FROM public.tenant_migration_markers WHERE schema_name = $1`, schema)
		_, _ = pool.Exec(c, `DROP SCHEMA IF EXISTS `+pgx.Identifier{schema}.Sanitize()+` CASCADE`)
		_, _ = pool.Exec(c, `DELETE FROM projects WHERE id = $1`, projectID)
		_, _ = pool.Exec(c, `DELETE FROM platform_users WHERE id = $1`, ownerID)
	})
	if _, err := pool.Exec(ctx, `CREATE SCHEMA `+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}

	exec := NewMigrationExecutor(pool, "", nil)
	tableExists := func(name string) bool {
		var ok bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, schema+"."+name).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}
	history := func() int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.tenant_migrations WHERE project_id = $1`, projectID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	const v1 = `CREATE TABLE listings (id int PRIMARY KEY); INSERT INTO listings VALUES (1);`
	applied, err := exec.ApplyDedicated(ctx, pool, projectID, schema, 1, "listings", v1)
	if err != nil || !applied {
		t.Fatalf("apply v1: applied=%v err=%v", applied, err)
	}
	if !tableExists("listings") || history() != 1 {
		t.Fatalf("v1: table=%v history=%d", tableExists("listings"), history())
	}

	// Re-run: no-op. Changed SQL: mismatch.
	if applied, err := exec.ApplyDedicated(ctx, pool, projectID, schema, 1, "listings", v1); err != nil || applied {
		t.Fatalf("re-run v1: applied=%v err=%v", applied, err)
	}
	if _, err := exec.ApplyDedicated(ctx, pool, projectID, schema, 1, "listings", v1+" SELECT 1;"); !errors.Is(err, ErrMigrationChecksumMismatch) {
		t.Fatalf("changed v1: err=%v, want checksum mismatch", err)
	}

	// A failing body leaves nothing behind: no table, no marker, no history.
	if _, err := exec.ApplyDedicated(ctx, pool, projectID, schema, 3, "bad", `CREATE TABLE half (id int); SELECT 1/0;`); err == nil {
		t.Fatal("failing body must error")
	}
	var markers int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.tenant_migration_markers WHERE schema_name = $1 AND version = 3`, schema).Scan(&markers); err != nil {
		t.Fatal(err)
	}
	if tableExists("half") || markers != 0 || history() != 1 {
		t.Fatalf("failed v3 left state: table=%v markers=%d history=%d", tableExists("half"), markers, history())
	}

	// Recovery: v2 marked on the dedicated side, no history row yet.
	const v2 = `CREATE TABLE rerun_guard (id int);`
	if _, err := pool.Exec(ctx, `INSERT INTO public.tenant_migration_markers (schema_name, version, checksum) VALUES ($1, 2, $2)`, schema, MigrationChecksum(v2)); err != nil {
		t.Fatal(err)
	}
	if applied, err := exec.ApplyDedicated(ctx, pool, projectID, schema, 2, "rerun", v2); err != nil || !applied {
		t.Fatalf("recover v2: applied=%v err=%v", applied, err)
	}
	if tableExists("rerun_guard") {
		t.Fatal("a marked migration's body ran again")
	}
	if history() != 2 {
		t.Fatalf("history = %d, want 2", history())
	}

	// Validator still applies.
	if _, err := exec.ApplyDedicated(ctx, pool, projectID, schema, 4, "grant", `GRANT SELECT ON listings TO PUBLIC;`); err == nil {
		t.Fatal("GRANT must be refused")
	}
}
