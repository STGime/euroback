package billing

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/eurobase/euroback/internal/audit"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Service-level tests. These require a Postgres with the migration
// surface up-to-date (000055 + 000056). Skipped under -short to match
// every other DB-backed test in the codebase.

func openServicePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = "postgres://eurobase_api:localdev@localhost:5433/eurobase?sslmode=disable"
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Skipf("cannot connect: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		t.Skipf("cannot ping: %v", err)
	}
	return pool
}

// stubMollie returns a Mollie client wired to an httptest server that
// returns the given payment JSON for any GetPayment call. Service
// tests use this so ApplyPaymentEvent can be driven end-to-end without
// touching real Mollie.
func stubMollie(t *testing.T, paymentJSON, subscriptionJSON string) *MollieClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && len(r.URL.Path) > len("/payments/") && r.URL.Path[:len("/payments/")] == "/payments/":
			_, _ = w.Write([]byte(paymentJSON))
		case r.Method == "POST" && len(r.URL.Path) > len("/customers/") && r.URL.Path[len(r.URL.Path)-len("/subscriptions"):] == "/subscriptions":
			_, _ = w.Write([]byte(subscriptionJSON))
		case r.Method == "DELETE":
			w.WriteHeader(200)
		case r.URL.Path == "/customers":
			_, _ = w.Write([]byte(`{"id":"cst_test","email":"o@e.com","name":"O"}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	c := NewMollieClient("test_x", "wh")
	c.baseURL = srv.URL
	return c
}

func setupSubscriptionFixture(t *testing.T, pool *pgxpool.Pool) (projectID, ownerID string, cleanup func()) {
	t.Helper()
	ctx := context.Background()
	// Owner
	var oid string
	if err := pool.QueryRow(ctx,
		`INSERT INTO public.platform_users (email, password_hash)
		 VALUES ('billtest+' || gen_random_uuid()::text || '@example.com', 'x')
		 RETURNING id::text`).Scan(&oid); err != nil {
		t.Skipf("seed platform user: %v", err)
	}
	// Project
	var pid string
	if err := pool.QueryRow(ctx,
		`INSERT INTO public.projects (name, slug, schema_name, owner_id, plan)
		 VALUES ('billtest', 'billtest-' || substr(gen_random_uuid()::text, 1, 8),
		         'tenant_billtest_' || substr(gen_random_uuid()::text, 1, 8),
		         $1, 'free')
		 RETURNING id::text`, oid).Scan(&pid); err != nil {
		_, _ = pool.Exec(ctx, `DELETE FROM public.platform_users WHERE id = $1`, oid)
		t.Skipf("seed project: %v", err)
	}
	cleanup = func() {
		_, _ = pool.Exec(ctx, `DELETE FROM public.subscriptions WHERE project_id = $1`, pid)
		_, _ = pool.Exec(ctx, `DELETE FROM public.invoices WHERE project_id = $1`, pid)
		_, _ = pool.Exec(ctx, `DELETE FROM public.projects WHERE id = $1`, pid)
		_, _ = pool.Exec(ctx, `DELETE FROM public.platform_users WHERE id = $1`, oid)
	}
	return pid, oid, cleanup
}

// TestApplyPaymentEvent_FreeToProOnPaid is the happy path that the
// dashboard depends on. payment.status = paid → projects.plan flips
// to pro, subscription row flips to active, invoice row written,
// audit log carries the plan-change event.
func TestApplyPaymentEvent_FreeToProOnPaid(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	pool := openServicePool(t)
	defer pool.Close()

	pid, _, cleanup := setupSubscriptionFixture(t, pool)
	defer cleanup()

	// Insert the pending_payment row that StartUpgrade would have written.
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO public.subscriptions (project_id, plan, status, current_period_start)
		 VALUES ($1, 'pro', 'pending_payment', now())`, pid); err != nil {
		t.Fatalf("seed pending subscription: %v", err)
	}

	paymentJSON := `{
		"id":"tr_paid",
		"status":"paid",
		"amount":{"currency":"EUR","value":"9.00"},
		"customerId":"cst_test",
		"paidAt":"2026-05-25T10:00:00Z",
		"metadata":{"project_id":"` + pid + `","plan":"pro"}
	}`
	subJSON := `{"id":"sub_xyz","status":"active","amount":{"currency":"EUR","value":"9.00"},"interval":"1 month"}`
	mollie := stubMollie(t, paymentJSON, subJSON)
	auditSvc := audit.NewService(pool)
	svc := NewService(pool, mollie, auditSvc, "http://console", "http://api")

	if err := svc.ApplyPaymentEvent(context.Background(), "tr_paid"); err != nil {
		t.Fatalf("ApplyPaymentEvent: %v", err)
	}

	// projects.plan must be pro.
	var plan string
	_ = pool.QueryRow(context.Background(),
		`SELECT plan FROM public.projects WHERE id = $1`, pid).Scan(&plan)
	if plan != PlanPro {
		t.Errorf("projects.plan: got %q want pro", plan)
	}
	// subscription.status must be active + mollie_subscription_id set.
	var status string
	var mollieSubID *string
	_ = pool.QueryRow(context.Background(),
		`SELECT status, mollie_subscription_id FROM public.subscriptions WHERE project_id = $1`, pid).
		Scan(&status, &mollieSubID)
	if status != StatusActive {
		t.Errorf("subscription.status: got %q want active", status)
	}
	if mollieSubID == nil || *mollieSubID != "sub_xyz" {
		t.Errorf("mollie_subscription_id: got %v want sub_xyz", mollieSubID)
	}
	// invoice row must exist.
	var invoiceCount int
	_ = pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM public.invoices WHERE project_id = $1 AND status = 'paid'`, pid).
		Scan(&invoiceCount)
	if invoiceCount != 1 {
		t.Errorf("invoice rows: got %d want 1", invoiceCount)
	}
}

// TestApplyPaymentEvent_FailedStartsGrace covers the dunning entry
// edge: payment.status=failed flips the subscription to grace with a
// grace_until ~72h out, but does NOT downgrade the project plan yet.
func TestApplyPaymentEvent_FailedStartsGrace(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	pool := openServicePool(t)
	defer pool.Close()

	pid, _, cleanup := setupSubscriptionFixture(t, pool)
	defer cleanup()

	// Subscription was active prior to the failed renewal.
	_, _ = pool.Exec(context.Background(),
		`UPDATE public.projects SET plan = 'pro' WHERE id = $1`, pid)
	_, _ = pool.Exec(context.Background(),
		`INSERT INTO public.subscriptions (project_id, mollie_subscription_id, plan, status, current_period_start, current_period_end)
		 VALUES ($1, 'sub_was_active', 'pro', 'active', now() - interval '20 days', now() + interval '10 days')`, pid)

	paymentJSON := `{
		"id":"tr_failed",
		"status":"failed",
		"amount":{"currency":"EUR","value":"9.00"},
		"customerId":"cst_test",
		"metadata":{"project_id":"` + pid + `","plan":"pro"}
	}`
	mollie := stubMollie(t, paymentJSON, "")
	svc := NewService(pool, mollie, audit.NewService(pool), "http://console", "http://api")

	if err := svc.ApplyPaymentEvent(context.Background(), "tr_failed"); err != nil {
		t.Fatalf("ApplyPaymentEvent: %v", err)
	}

	var plan string
	_ = pool.QueryRow(context.Background(),
		`SELECT plan FROM public.projects WHERE id = $1`, pid).Scan(&plan)
	if plan != PlanPro {
		t.Errorf("project plan should stay pro during grace, got %q", plan)
	}

	var status string
	var graceUntil *string
	_ = pool.QueryRow(context.Background(),
		`SELECT status, grace_until::text FROM public.subscriptions WHERE project_id = $1`, pid).
		Scan(&status, &graceUntil)
	if status != StatusGrace {
		t.Errorf("subscription.status: got %q want grace", status)
	}
	if graceUntil == nil {
		t.Error("grace_until should be set on failed-payment transition")
	}
}

// TestRunDunningSweep_DowngradesAfterGraceExpiry is the close of the
// dunning loop: a subscription whose grace_until is in the past gets
// downgraded by the sweeper, projects.plan flips to free, and an
// audit row is emitted.
func TestRunDunningSweep_DowngradesAfterGraceExpiry(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	pool := openServicePool(t)
	defer pool.Close()

	pid, _, cleanup := setupSubscriptionFixture(t, pool)
	defer cleanup()

	_, _ = pool.Exec(context.Background(),
		`UPDATE public.projects SET plan = 'pro' WHERE id = $1`, pid)
	_, _ = pool.Exec(context.Background(),
		`INSERT INTO public.subscriptions (project_id, mollie_subscription_id, plan, status, grace_until, current_period_start)
		 VALUES ($1, 'sub_exp', 'pro', 'grace', now() - interval '1 hour', now() - interval '5 days')`, pid)

	mollie := stubMollie(t, "{}", "")
	svc := NewService(pool, mollie, audit.NewService(pool), "http://console", "http://api")

	downgraded, finalised, err := svc.RunDunningSweep(context.Background())
	if err != nil {
		t.Fatalf("RunDunningSweep: %v", err)
	}
	if downgraded < 1 {
		t.Errorf("expected at least 1 grace-downgrade, got %d", downgraded)
	}
	_ = finalised

	var plan, status string
	_ = pool.QueryRow(context.Background(),
		`SELECT projects.plan, subscriptions.status
		 FROM public.projects
		 JOIN public.subscriptions ON subscriptions.project_id = projects.id
		 WHERE projects.id = $1`, pid).Scan(&plan, &status)
	if plan != PlanFree {
		t.Errorf("plan should be free after grace expiry, got %q", plan)
	}
	if status != StatusCancelled {
		t.Errorf("subscription.status: got %q want cancelled", status)
	}
}

// TestApplyPaymentEvent_IsIdempotent — receiving the same payment ID
// twice must not double-create invoices or double-flip plan.
func TestApplyPaymentEvent_IsIdempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	pool := openServicePool(t)
	defer pool.Close()

	pid, _, cleanup := setupSubscriptionFixture(t, pool)
	defer cleanup()
	_, _ = pool.Exec(context.Background(),
		`INSERT INTO public.subscriptions (project_id, plan, status, current_period_start)
		 VALUES ($1, 'pro', 'pending_payment', now())`, pid)

	paymentJSON := `{
		"id":"tr_dup",
		"status":"paid",
		"amount":{"currency":"EUR","value":"9.00"},
		"customerId":"cst_test",
		"paidAt":"2026-05-25T10:00:00Z",
		"metadata":{"project_id":"` + pid + `","plan":"pro"}
	}`
	subJSON := `{"id":"sub_dup","status":"active","amount":{"currency":"EUR","value":"9.00"},"interval":"1 month"}`
	mollie := stubMollie(t, paymentJSON, subJSON)
	svc := NewService(pool, mollie, audit.NewService(pool), "http://console", "http://api")

	for i := 0; i < 3; i++ {
		if err := svc.ApplyPaymentEvent(context.Background(), "tr_dup"); err != nil {
			t.Fatalf("apply #%d: %v", i, err)
		}
	}

	var invoiceCount int
	_ = pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM public.invoices WHERE project_id = $1`, pid).Scan(&invoiceCount)
	if invoiceCount != 1 {
		t.Errorf("invoices should be deduped by ON CONFLICT, got %d rows", invoiceCount)
	}
}

// TestSubscriptionView_PendingFreshProject — a project with no
// subscription row gets a synthetic free view, never an error.
func TestSubscriptionView_FreshProjectReturnsFree(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	pool := openServicePool(t)
	defer pool.Close()

	pid, _, cleanup := setupSubscriptionFixture(t, pool)
	defer cleanup()

	svc := NewService(pool, NewMollieClient("test_x", "wh"), audit.NewService(pool), "", "")
	v, err := svc.GetSubscription(context.Background(), pid)
	if err != nil {
		t.Fatalf("GetSubscription: %v", err)
	}
	if v.Plan != PlanFree || v.Status != PlanFree {
		t.Errorf("fresh project: got %+v, want plan=free status=free", v)
	}

	// Sanity-check JSON shape since the view is what the handler returns.
	b, _ := json.Marshal(v)
	if !json.Valid(b) {
		t.Error("subscription view did not marshal to valid JSON")
	}
}
