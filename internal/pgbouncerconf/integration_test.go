package pgbouncerconf

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/eurobase/euroback/internal/tenantlogin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// End-to-end against a real PgBouncer built from deploy/docker/Dockerfile.pgbouncer
// in front of a migrated PG16 — run by scripts/test-pgbouncer.sh, which
// sets:
//
//	PGB_TEST_POOLED_GATEWAY  postgres://eurobase_gateway:…@localhost:6432/eurobase
//	PGB_TEST_POOLED_BASE     postgres://x:x@localhost:6432/eurobase (user/pass replaced)
//	PGB_TEST_DEV_URL         direct developer URL (provisions a tenant as migrator)
//	PGB_TEST_SECRET          FUNC_PASSWORD_SECRET used by the pooler's userlist
func TestPgBouncerEndToEnd(t *testing.T) {
	gw, base, dev, secret := os.Getenv("PGB_TEST_POOLED_GATEWAY"), os.Getenv("PGB_TEST_POOLED_BASE"),
		os.Getenv("PGB_TEST_DEV_URL"), os.Getenv("PGB_TEST_SECRET")
	if gw == "" || base == "" || dev == "" || secret == "" {
		t.Skip("run via scripts/test-pgbouncer.sh")
	}
	ctx := context.Background()

	t.Run("platform role via pooler", func(t *testing.T) {
		c, err := pgx.Connect(ctx, gw)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close(ctx)
		var u string
		if err := c.QueryRow(ctx, "SELECT current_user").Scan(&u); err != nil {
			t.Fatal(err)
		}
		if u != "eurobase_gateway" {
			t.Errorf("current_user = %q", u)
		}
	})

	// A tenant provisioned now becomes reachable once the sidecar picks
	// it up — with a derived SCRAM verifier, no plaintext in the pooler.
	t.Run("new tenant via SCRAM pass-through", func(t *testing.T) {
		devPool, err := pgxpool.New(ctx, dev)
		if err != nil {
			t.Fatal(err)
		}
		defer devPool.Close()
		id := uuid.NewString()
		schema := "tenant_" + strings.ReplaceAll(id, "-", "_")
		tx, err := devPool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, q := range []string{
			"SET LOCAL ROLE eurobase_migrator",
			`INSERT INTO platform_users (id, email) VALUES ('` + id + `', '` + id + `@pgb.test')`,
			`INSERT INTO projects (id, owner_id, name, slug, schema_name, s3_bucket, region, plan, status)
			 VALUES ('` + id + `', '` + id + `', 'pgb', 'pgb-` + id[:8] + `', 'tmp', 'b-` + id[:8] + `', 'fr-par', 'free', 'provisioning')`,
			`SELECT provision_tenant('` + id + `', 'pgb', 'free')`,
		} {
			if _, err := tx.Exec(ctx, q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		ens, err := tenantlogin.NewEnsurer(devPool, devPool.Config().ConnConfig.Database, []byte(secret))
		if err != nil {
			t.Fatal(err)
		}
		if err := ens.EnsureOne(ctx, schema); err != nil {
			t.Fatal(err)
		}

		cfg, err := pgx.ParseConfig(base)
		if err != nil {
			t.Fatal(err)
		}
		cfg.User, cfg.Password = tenantlogin.FuncRole(schema), tenantlogin.FuncPassword([]byte(secret), schema)
		var who string
		deadline := time.Now().Add(20 * time.Second)
		for {
			c, err := pgx.ConnectConfig(ctx, cfg)
			if err == nil {
				err = c.QueryRow(ctx, "SELECT session_user").Scan(&who)
				c.Close(ctx)
			}
			if err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("tenant login via pooler never succeeded: %v", err)
			}
			time.Sleep(time.Second)
		}
		if who != schema+"_func" {
			t.Errorf("session_user = %q, want %q", who, schema+"_func")
		}

		// Wrong password is refused by the pooler.
		cfg.Password = "wrong"
		if c, err := pgx.ConnectConfig(ctx, cfg); err == nil {
			c.Close(ctx)
			t.Error("pooler accepted a wrong tenant password")
		}
	})

	// pgx's default mode caches named prepared statements; through
	// transaction pooling with a reset after every transaction they must
	// keep working across many transactions and clients.
	t.Run("prepared statements across transactions", func(t *testing.T) {
		pool, err := pgxpool.New(ctx, gw)
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()
		for i := 0; i < 50; i++ {
			var n int
			if err := pool.QueryRow(ctx, "SELECT $1::int + 1", i).Scan(&n); err != nil {
				t.Fatalf("iteration %d: %v", i, err)
			}
			if n != i+1 {
				t.Fatalf("iteration %d: got %d", i, n)
			}
		}
	})

	// Session state set by one client never reaches another client that
	// gets the same server connection (gateway pool_size=1 in the test).
	t.Run("no session state between clients", func(t *testing.T) {
		a, err := pgx.Connect(ctx, gw)
		if err != nil {
			t.Fatal(err)
		}
		defer a.Close(ctx)
		b, err := pgx.Connect(ctx, gw)
		if err != nil {
			t.Fatal(err)
		}
		defer b.Close(ctx)
		for i := 0; i < 10; i++ {
			if _, err := a.Exec(ctx, "SET work_mem = '64MB'"); err != nil {
				t.Fatal(err)
			}
			if _, err := a.Exec(ctx, "SELECT set_config('app.end_user_id', '00000000-0000-0000-0000-000000000009', false)"); err != nil {
				t.Fatal(err)
			}
			var wm, uid string
			if err := b.QueryRow(ctx, "SELECT current_setting('work_mem'), coalesce(current_setting('app.end_user_id', true), '')").Scan(&wm, &uid); err != nil {
				t.Fatal(err)
			}
			if wm == "64MB" || uid != "" {
				t.Fatalf("session state crossed clients: work_mem=%s end_user_id=%q", wm, uid)
			}
		}
	})
}
