package breach

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// setupBreachTest provisions two projects so we can exercise the
// cross-tenant scope check (PR #219 review). Skips when no test DB.
func setupBreachTest(t *testing.T) (*Service, string, string, *pgxpool.Pool) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgres://eurobase_api:localdev@localhost:5433/eurobase?sslmode=disable"
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Skipf("cannot connect to test database: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("cannot ping test database: %v", err)
	}

	hankoID := fmt.Sprintf("test-breach-%d", os.Getpid())
	var ownerID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO platform_users (hanko_user_id, email)
		 VALUES ($1, $2)
		 ON CONFLICT (hanko_user_id) DO UPDATE SET email = EXCLUDED.email
		 RETURNING id`,
		hankoID, "breachtest@eurobase.app",
	).Scan(&ownerID); err != nil {
		pool.Close()
		t.Skipf("cannot create test platform user: %v", err)
	}

	mkProject := func(slug string) string {
		var id string
		if err := pool.QueryRow(ctx,
			`INSERT INTO projects (owner_id, name, slug, schema_name, s3_bucket, region, plan, status)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, 'provisioning')
			 RETURNING id`,
			ownerID, slug, slug, "tenant_"+slug, "bucket-"+slug, "fr-par", "free",
		).Scan(&id); err != nil {
			pool.Close()
			t.Skipf("cannot create test project %s: %v", slug, err)
		}
		return id
	}
	projA := mkProject(fmt.Sprintf("breach-a-%d", os.Getpid()))
	projB := mkProject(fmt.Sprintf("breach-b-%d", os.Getpid()))

	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM public.breach_register WHERE project_id IN ($1, $2)`, projA, projB)
		_, _ = pool.Exec(ctx, `DELETE FROM projects WHERE id IN ($1, $2)`, projA, projB)
		_, _ = pool.Exec(ctx, `DELETE FROM platform_users WHERE hanko_user_id = $1`, hankoID)
		pool.Close()
	})

	return NewService(pool, nil, nil), projA, projB, pool
}

// TestService_CrossTenantLookupReturnsNotFound is the regression for the
// PR #219 IDOR: open an incident in project A, then assert that *every*
// project-B-scoped read or mutation comes back empty / not-found.
func TestService_CrossTenantLookupReturnsNotFound(t *testing.T) {
	svc, projA, projB, _ := setupBreachTest(t)
	ctx := context.Background()

	// Open a project-A-scoped incident the way HandleOpen would: caller
	// anchors ProjectID at the URL project.
	entry, err := svc.Open(ctx, OpenInput{
		Title:       "A's incident",
		Description: "scoped to project A",
		ProjectID:   &projA,
	}, "", "actor@a")
	if err != nil {
		t.Fatalf("open A: %v", err)
	}
	incidentA := entry.IncidentID

	// Same-project lookup is the happy path.
	if got, err := svc.Latest(ctx, projA, incidentA); err != nil || got == nil {
		t.Fatalf("Latest(A, A's incident) = (%v, %v); want non-nil", got, err)
	}
	if got, err := svc.History(ctx, projA, incidentA); err != nil || len(got) == 0 {
		t.Fatalf("History(A, A's incident) = (%d, %v); want non-empty", len(got), err)
	}

	// Cross-tenant lookups (project B reaching for A's incident) must all
	// come back empty / not-found.
	if got, err := svc.Latest(ctx, projB, incidentA); err != nil || got != nil {
		t.Errorf("Latest(B, A's incident) = (%+v, %v); want (nil, nil)", got, err)
	}
	if got, err := svc.History(ctx, projB, incidentA); err != nil || len(got) != 0 {
		t.Errorf("History(B, A's incident) = (%d rows, %v); want (0, nil)", len(got), err)
	}
	if _, err := svc.Update(ctx, projB, incidentA, UpdateInput{Note: "tamper"}, "", "evil@b"); err == nil {
		t.Errorf("Update(B, A's incident) succeeded; want incident-not-found error")
	}
	if _, err := svc.MarkNotification(ctx, projB, incidentA, "customers", "", "tamper", "", "evil@b"); err == nil {
		t.Errorf("MarkNotification(B, A's incident) succeeded; want incident-not-found error")
	}
	if _, err := svc.Close(ctx, projB, incidentA, StatusClosed, "tamper", "", "evil@b"); err == nil {
		t.Errorf("Close(B, A's incident) succeeded; want incident-not-found error")
	}

	// And the register itself must still only carry A's row — no smuggled-in
	// B-owned writes from the failed attempts above.
	if got, err := svc.History(ctx, projA, incidentA); err != nil || len(got) != 1 {
		t.Errorf("History(A, A's incident) after attempted cross-tenant writes = (%d rows, %v); want (1, nil)", len(got), err)
	}
}
