package events

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// loadFromDB as eurobase_gateway (the gateway pool) on a migrated
// database: enabled webhooks with events, and enabled triggers of active
// functions only. EVENTS_TEST_GATEWAY_URL; skipped when unset.
func TestLoadSubscriptionsFromDB(t *testing.T) {
	dsn := os.Getenv("EVENTS_TEST_GATEWAY_URL")
	if dsn == "" {
		t.Skip("EVENTS_TEST_GATEWAY_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	u := strings.ReplaceAll(uuid.NewString()[:12], "-", "")
	var owner, project string
	if err := pool.QueryRow(ctx, `INSERT INTO public.platform_users (email) VALUES ($1) RETURNING id`, "evload-"+u+"@test.invalid").Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO public.projects (owner_id, name, slug, schema_name, s3_bucket, region, plan, status)
		 VALUES ($1, 'x', $2, $3, $4, 'fr-par', 'free', 'active') RETURNING id`,
		owner, "evload-"+u, "tenant_evload_"+u, "b-evload-"+u).Scan(&project); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM public.projects WHERE id = $1`, project)
		_, _ = pool.Exec(ctx, `DELETE FROM public.platform_users WHERE id = $1`, owner)
	}()

	var on, off, empty string
	for _, w := range []struct {
		dst     *string
		events  string
		enabled bool
	}{{&on, "{db.insert,storage.upload}", true}, {&off, "{db.insert}", false}, {&empty, "{}", true}} {
		if err := pool.QueryRow(ctx, `INSERT INTO public.webhooks (project_id, url, events, secret, enabled) VALUES ($1, 'https://example.com', $2, 's', $3) RETURNING id`,
			project, w.events, w.enabled).Scan(w.dst); err != nil {
			t.Fatal(err)
		}
	}
	var active, inactive string
	for _, f := range []struct {
		dst    *string
		name   string
		status string
	}{{&active, "a", "active"}, {&inactive, "b", "disabled"}} {
		if err := pool.QueryRow(ctx, `INSERT INTO public.edge_functions (project_id, name, code, status) VALUES ($1, $2, 'x', $3) RETURNING id`,
			project, f.name, f.status).Scan(f.dst); err != nil {
			t.Fatal(err)
		}
	}
	var trigOn, trigOff, trigInactiveFn string
	for _, tr := range []struct {
		dst     *string
		fn      string
		table   string
		enabled bool
	}{{&trigOn, active, "todos", true}, {&trigOff, active, "notes", false}, {&trigInactiveFn, inactive, "todos", true}} {
		if err := pool.QueryRow(ctx, `INSERT INTO public.function_triggers (function_id, project_id, table_name, events, enabled) VALUES ($1, $2, $3, '{INSERT}', $4) RETURNING id`,
			tr.fn, project, tr.table, tr.enabled).Scan(tr.dst); err != nil {
			t.Fatal(err)
		}
	}

	e := NewEmitter(pool, nil)
	hooks, trigs, err := e.loadFromDB(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	if len(hooks) != 1 || hooks[0].id != on || !slices.Equal(hooks[0].events, []string{"db.insert", "storage.upload"}) {
		t.Fatalf("webhooks = %+v, want only %s", hooks, on)
	}
	if len(trigs) != 1 || trigs[0].id != trigOn || trigs[0].table != "todos" {
		t.Fatalf("triggers = %+v, want only %s", trigs, trigOn)
	}
}
