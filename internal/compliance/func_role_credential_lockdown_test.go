package compliance

// Regression test for the credential-table lockdown (migration 000147).
//
// A tenant RPC / edge-function body runs as the per-tenant <schema>_func
// role. The credential tables (refresh_tokens, email_tokens,
// vault_secrets) are guarded only by the RLS predicate
// is_internal_auth_path() = current_setting('app.intent') =
// 'internal_auth_path' — a GUC a function body can set in its own
// transaction. Before 000147 the func role also held DML on those tables,
// so flipping the GUC exposed and let it forge credential rows (account
// takeover). 000147 revokes the grant, so the access is denied before RLS
// is even consulted, whatever the GUC says.
//
// Runs against the migrated production-role PG16 in compliance-export.yml
// (TEAM_EXPORT_TEST_ADMIN_URL is the superuser that provisions tenants).
// The "_RLS" suffix keeps it in that job's -run filter.

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestFuncRoleCannotReachCredentialTables_RLS(t *testing.T) {
	adminURL := os.Getenv("TEAM_EXPORT_TEST_ADMIN_URL")
	if adminURL == "" {
		t.Skip("TEAM_EXPORT_TEST_ADMIN_URL not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()

	// Provision a throwaway tenant via the real provision_tenant path.
	projectID := uuid.NewString()
	schema := "tenant_" + strings.ReplaceAll(projectID, "-", "_")
	funcRole := schema + "_func"
	owner := uuid.NewString()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`INSERT INTO public.platform_users (id, email) VALUES ($1, $2)`, owner, "fnlock-"+owner+"@test.invalid")
	exec(`INSERT INTO public.projects (id, owner_id, name, slug, schema_name, s3_bucket, region, plan, status)
	      VALUES ($1,$2,'fnlock',$3,$4,$5,'fr-par','free','active')`, projectID, owner, "fnlock-"+uuid.NewString()[:8], schema, "b-fnlock-"+uuid.NewString()[:8])
	exec(`SELECT public.provision_tenant($1::uuid, 'fnlock', 'free')`, projectID)
	t.Cleanup(func() {
		// Mirror public.deprovision_tenant: drop the schema and the rows.
		// The NOLOGIN <schema>_func role is intentionally left (its ACL
		// dependency on schema public blocks DROP ROLE; the platform's own
		// deprovision_tenant leaves it too).
		_, _ = admin.Exec(ctx, fmt.Sprintf(`DROP SCHEMA IF EXISTS %s CASCADE`, pgx.Identifier{schema}.Sanitize()))
		_, _ = admin.Exec(ctx, `DELETE FROM public.projects WHERE id = $1`, projectID)
		_, _ = admin.Exec(ctx, `DELETE FROM public.platform_users WHERE id = $1`, owner)
	})

	// Seed a victim + a credential row (as the owner/superuser, RLS bypassed).
	victim := uuid.NewString()
	exec(fmt.Sprintf(`INSERT INTO %s.users (id, email) VALUES ($1, 'victim@test.invalid')`, pgx.Identifier{schema}.Sanitize()), victim)
	exec(fmt.Sprintf(`INSERT INTO %s.refresh_tokens (id, user_id, token_hash, expires_at) VALUES (gen_random_uuid(), $1, 'seed', now() + interval '1 day')`, pgx.Identifier{schema}.Sanitize()), victim)

	// The grant itself is gone.
	for _, tbl := range []string{"refresh_tokens", "email_tokens", "vault_secrets"} {
		for _, priv := range []string{"SELECT", "INSERT", "UPDATE", "DELETE"} {
			var has bool
			if err := admin.QueryRow(ctx, `SELECT has_table_privilege($1, $2, $3)`, funcRole, schema+"."+tbl, priv).Scan(&has); err != nil {
				t.Fatalf("has_table_privilege(%s,%s): %v", tbl, priv, err)
			}
			if has {
				t.Errorf("%s still has %s on %s.%s", funcRole, priv, schema, tbl)
			}
		}
	}

	// Simulate a function body: as the func role, flip the gate and try to
	// read / forge. Must be permission denied, not rows.
	asFunc := func(t *testing.T, body string) error {
		t.Helper()
		tx, err := admin.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, `SET LOCAL ROLE `+pgx.Identifier{funcRole}.Sanitize()); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(`SET LOCAL search_path TO %s, public`, pgx.Identifier{schema}.Sanitize())); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('app.intent','internal_auth_path',true)`); err != nil {
			t.Fatal(err)
		}
		_, err = tx.Exec(ctx, body)
		return err
	}

	isDenied := func(err error) bool {
		return err != nil && strings.Contains(strings.ToLower(err.Error()), "permission denied")
	}

	if err := asFunc(t, `SELECT count(*) FROM refresh_tokens`); !isDenied(err) {
		t.Errorf("read of refresh_tokens with the GUC flipped: err = %v, want permission denied", err)
	}
	if err := asFunc(t, fmt.Sprintf(`INSERT INTO refresh_tokens (id, user_id, token_hash, expires_at) VALUES (gen_random_uuid(), '%s', 'forged', now() + interval '30 days')`, victim)); !isDenied(err) {
		t.Errorf("forge into refresh_tokens with the GUC flipped: err = %v, want permission denied", err)
	}
	if err := asFunc(t, `SELECT count(*) FROM email_tokens`); !isDenied(err) {
		t.Errorf("read of email_tokens: err = %v, want permission denied", err)
	}
	if err := asFunc(t, `SELECT count(*) FROM vault_secrets`); !isDenied(err) {
		t.Errorf("read of vault_secrets: err = %v, want permission denied", err)
	}

	// Sanity: the func role still works on its own tables.
	if err := asFunc(t, `INSERT INTO todos (id, title) VALUES (gen_random_uuid(), 'ok')`); err != nil {
		t.Errorf("func role can no longer write its own tables: %v", err)
	}
}
