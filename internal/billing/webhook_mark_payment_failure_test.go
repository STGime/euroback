package billing

// Integration tests for markPaymentFailure — the code path Mollie hits on
// every payment problem. Closes #468.
//
// Why not table-driven pgxmock instead: the whole point of these tests
// (per #468) is to lock in SQL correctness — specifically the two guards
// that keep silently disappearing across refactors:
//   1. UPDATE public.subscriptions ... WHERE ... AND status = 'incomplete'
//      (case g — defence against delayed webhook redelivery cancelling a
//      live sub).
//   2. UPDATE public.subscriptions ... WHERE ... AND status = 'active'
//      (case f — recurring-failure path must not accidentally hit the
//      abandonment cleanup and cancel live subs).
//
// A pgxmock exact-string match would satisfy the letter of those tests
// but not the spirit — a refactor that changes the query text while
// keeping the guard would need a matching pgxmock update, so the guard
// would still get quietly moved without the test noticing. Real Postgres
// exercises the actual constraint semantics: pass a wrong-status row,
// the UPDATE no-ops, the assertion catches it.
//
// Test gating matches the existing repo idiom (see
// internal/audit/access_test.go): skipped under -short, skipped if no
// DATABASE_URL / can't reach a DB. Auto-graduates to CI when #475
// (Postgres in test-go) lands. Runs today against a local dev DB via
// `go test ./internal/billing/... -run TestMarkPaymentFailure`.

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/eurobase/euroback/internal/billing/mollie"
	"github.com/jackc/pgx/v5/pgxpool"
)

// paymentFailureFixture is the small piece of state each subtest seeds
// before calling markPaymentFailure. Every field is optional so the
// same setup works for every case in the matrix — subtests that only
// care about the invoice leave subscriptionSeed and pendingSeed as
// their zero values.
type paymentFailureFixture struct {
	pool          *pgxpool.Pool
	projectID     string
	ownerID       string
	subscriptionID       string // local subscriptions.id (used in metadata['subscription_id'])
	mollieSubscriptionID string // subscriptions.mollie_subscription_id (used in payment.SubscriptionID for recurring)
	pendingID     string
	molliePaymentID string // shared with the invoices row and payment.ID
}

func setupPaymentFailureFixture(t *testing.T) *paymentFailureFixture {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping DB integration test in -short mode")
	}

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		// Same local-dev DSN convention audit/access_test.go uses, so a
		// developer with the standard docker-compose stack can just run
		// `go test ./internal/billing/... -run TestMarkPaymentFailure`.
		dsn = "postgres://eurobase_api:localdev@localhost:5433/eurobase?sslmode=disable"
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("cannot connect to test database: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("cannot ping test database: %v", err)
	}

	// Fixture PKs derived from PID + goroutine timestamp so parallel
	// package tests don't collide. We only need uniqueness, not
	// human-readability.
	unique := fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	hankoUserID := "test-billwh-" + unique
	slug := "test-billwh-" + unique

	var ownerID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO platform_users (hanko_user_id, email)
		 VALUES ($1, $2)
		 ON CONFLICT (hanko_user_id) DO UPDATE SET email = EXCLUDED.email
		 RETURNING id`,
		hankoUserID, "billwh-"+unique+"@eurobase.app",
	).Scan(&ownerID); err != nil {
		pool.Close()
		t.Skipf("cannot create test platform user: %v", err)
	}

	var projectID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO projects (owner_id, name, slug, schema_name, s3_bucket, region, plan, status)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, 'provisioning')
		 RETURNING id`,
		ownerID, "MarkPaymentFailure Test", slug, "tenant_test_billwh_"+unique, "eurobase-test-billwh-"+unique, "fr-par", "free",
	).Scan(&projectID); err != nil {
		pool.Close()
		t.Skipf("cannot create test project: %v", err)
	}

	fx := &paymentFailureFixture{
		pool:            pool,
		projectID:       projectID,
		ownerID:         ownerID,
		molliePaymentID: "tr_test_" + unique,
	}

	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM public.invoices WHERE project_id = $1`, projectID)
		_, _ = pool.Exec(ctx, `DELETE FROM public.subscriptions WHERE project_id = $1`, projectID)
		_, _ = pool.Exec(ctx, `DELETE FROM public.pending_projects WHERE owner_id = $1`, ownerID)
		_, _ = pool.Exec(ctx, `DELETE FROM public.projects WHERE id = $1`, projectID)
		_, _ = pool.Exec(ctx, `DELETE FROM public.platform_users WHERE id = $1`, ownerID)
		pool.Close()
	})

	return fx
}

// seedInvoice inserts a pending invoice keyed on molliePaymentID. Called
// by every subtest — every markPaymentFailure branch marks the invoice
// row.
func (fx *paymentFailureFixture) seedInvoice(t *testing.T) {
	t.Helper()
	if _, err := fx.pool.Exec(context.Background(),
		`INSERT INTO public.invoices (project_id, mollie_payment_id, amount_cents, currency, status)
		 VALUES ($1, $2, 1900, 'EUR', 'pending')`,
		fx.projectID, fx.molliePaymentID,
	); err != nil {
		t.Fatalf("seed invoice: %v", err)
	}
}

// seedSubscription inserts a subscriptions row at the given status and
// records fx.subscriptionID (used in payment.Metadata['subscription_id']).
// mollieSubID is written to mollie_subscription_id when non-empty; the
// recurring branch (case f) matches on that column.
func (fx *paymentFailureFixture) seedSubscription(t *testing.T, status, mollieSubID string) {
	t.Helper()
	var id string
	err := fx.pool.QueryRow(context.Background(),
		`INSERT INTO public.subscriptions (project_id, plan, status, mollie_subscription_id)
		 VALUES ($1, 'pro', $2, NULLIF($3, ''))
		 RETURNING id`,
		fx.projectID, status, mollieSubID,
	).Scan(&id)
	if err != nil {
		t.Fatalf("seed subscription (status=%s): %v", status, err)
	}
	fx.subscriptionID = id
	fx.mollieSubscriptionID = mollieSubID
}

// seedPendingProject inserts a pending_projects row and records
// fx.pendingID for payment.Metadata['pending_project_id'].
func (fx *paymentFailureFixture) seedPendingProject(t *testing.T) {
	t.Helper()
	var id string
	err := fx.pool.QueryRow(context.Background(),
		`INSERT INTO public.pending_projects (owner_id, name, slug, region, plan, mollie_payment_id)
		 VALUES ($1, $2, $3, 'fr-par', 'pro', $4)
		 RETURNING id`,
		fx.ownerID, "Pending "+fx.molliePaymentID, "pend-"+fx.molliePaymentID, fx.molliePaymentID,
	).Scan(&id)
	if err != nil {
		t.Fatalf("seed pending_project: %v", err)
	}
	fx.pendingID = id
}

// ── Assertion helpers — encode what each branch of markPaymentFailure
//    is supposed to do so the subtests read as intent, not SQL. ──

func (fx *paymentFailureFixture) assertInvoiceStatus(t *testing.T, want string) {
	t.Helper()
	var got string
	if err := fx.pool.QueryRow(context.Background(),
		`SELECT status FROM public.invoices WHERE mollie_payment_id = $1`,
		fx.molliePaymentID,
	).Scan(&got); err != nil {
		t.Fatalf("read invoice: %v", err)
	}
	if got != want {
		t.Errorf("invoice.status = %q, want %q", got, want)
	}
}

func (fx *paymentFailureFixture) assertSubscription(t *testing.T, wantStatus string, wantCanceledSet, wantPastDueSet bool) {
	t.Helper()
	if fx.subscriptionID == "" {
		t.Fatal("assertSubscription called without seedSubscription")
	}
	var status string
	var canceledAt, pastDueSince *time.Time
	if err := fx.pool.QueryRow(context.Background(),
		`SELECT status, canceled_at, past_due_since FROM public.subscriptions WHERE id = $1`,
		fx.subscriptionID,
	).Scan(&status, &canceledAt, &pastDueSince); err != nil {
		t.Fatalf("read subscription: %v", err)
	}
	if status != wantStatus {
		t.Errorf("subscription.status = %q, want %q", status, wantStatus)
	}
	if wantCanceledSet && canceledAt == nil {
		t.Error("subscription.canceled_at is NULL, want set")
	}
	if !wantCanceledSet && canceledAt != nil {
		t.Errorf("subscription.canceled_at = %v, want NULL", canceledAt)
	}
	if wantPastDueSet && pastDueSince == nil {
		t.Error("subscription.past_due_since is NULL, want set")
	}
	if !wantPastDueSet && pastDueSince != nil {
		t.Errorf("subscription.past_due_since = %v, want NULL", pastDueSince)
	}
}

func (fx *paymentFailureFixture) assertPendingProjectExists(t *testing.T, want bool) {
	t.Helper()
	if fx.pendingID == "" {
		t.Fatal("assertPendingProjectExists called without seedPendingProject")
	}
	var exists bool
	if err := fx.pool.QueryRow(context.Background(),
		`SELECT EXISTS(SELECT 1 FROM public.pending_projects WHERE id = $1)`,
		fx.pendingID,
	).Scan(&exists); err != nil {
		t.Fatalf("check pending_project: %v", err)
	}
	if exists != want {
		t.Errorf("pending_project exists = %v, want %v", exists, want)
	}
}

// ── The tests. Cases (a)/(b)/(c)/(d)/(e)/(f)/(g) from #468. ──

// TestMarkPaymentFailure_AbandonedCheckoutCancelsIncompleteSub covers
// (a) canceled, (b) failed, (c) expired — all three abandonment
// statuses run the same branch. Verifies invoice→failed AND the
// subscriptions row (referenced via metadata.subscription_id) goes
// canceled + canceled_at set.
func TestMarkPaymentFailure_AbandonedCheckoutCancelsIncompleteSub(t *testing.T) {
	for _, status := range []string{"canceled", "failed", "expired"} {
		t.Run(status, func(t *testing.T) {
			fx := setupPaymentFailureFixture(t)
			fx.seedInvoice(t)
			fx.seedSubscription(t, "incomplete", "")

			svc := &Service{pool: fx.pool}
			payment := &mollie.Payment{
				ID:       fx.molliePaymentID,
				Status:   status,
				Metadata: map[string]string{"subscription_id": fx.subscriptionID},
			}
			if err := svc.markPaymentFailure(context.Background(), payment); err != nil {
				t.Fatalf("markPaymentFailure: %v", err)
			}

			fx.assertInvoiceStatus(t, "failed")
			fx.assertSubscription(t, "canceled", true /*canceled_at*/, false /*past_due_since*/)
		})
	}
}

// TestMarkPaymentFailure_AbandonedCheckoutDeletesPendingProject —
// case (d). Metadata carries pending_project_id (not subscription_id);
// the pending_projects row must be deleted so the sweeper doesn't
// later log a false "Mollie took a payment we lost" warning.
func TestMarkPaymentFailure_AbandonedCheckoutDeletesPendingProject(t *testing.T) {
	fx := setupPaymentFailureFixture(t)
	fx.seedInvoice(t)
	fx.seedPendingProject(t)

	svc := &Service{pool: fx.pool}
	payment := &mollie.Payment{
		ID:       fx.molliePaymentID,
		Status:   "canceled",
		Metadata: map[string]string{"pending_project_id": fx.pendingID},
	}
	if err := svc.markPaymentFailure(context.Background(), payment); err != nil {
		t.Fatalf("markPaymentFailure: %v", err)
	}

	fx.assertInvoiceStatus(t, "failed")
	fx.assertPendingProjectExists(t, false)
}

// TestMarkPaymentFailure_AbandonedCheckoutEmptyMetadata — case (e).
// A failed payment with no subscription_id and no pending_project_id
// still marks the invoice failed; there's just nothing else to touch.
// Edge case (Mollie webhook redelivery after we've already cleaned up).
func TestMarkPaymentFailure_AbandonedCheckoutEmptyMetadata(t *testing.T) {
	fx := setupPaymentFailureFixture(t)
	fx.seedInvoice(t)

	svc := &Service{pool: fx.pool}
	payment := &mollie.Payment{
		ID:       fx.molliePaymentID,
		Status:   "canceled",
		Metadata: map[string]string{},
	}
	if err := svc.markPaymentFailure(context.Background(), payment); err != nil {
		t.Fatalf("markPaymentFailure: %v", err)
	}

	fx.assertInvoiceStatus(t, "failed")
}

// TestMarkPaymentFailure_RecurringFailureMarksPastDue — case (f).
// The recurring branch (payment.SubscriptionID set) must:
//   1. mark the subscription past_due + past_due_since (not canceled)
//   2. NOT touch the abandonment cleanup path (no pending_projects
//      delete, no incomplete→canceled UPDATE)
//
// This is the negative-assertion case in #468 — a refactor that
// accidentally shares the branch would cancel a live sub on a
// declined charge.
func TestMarkPaymentFailure_RecurringFailureMarksPastDue(t *testing.T) {
	fx := setupPaymentFailureFixture(t)
	fx.seedInvoice(t)
	fx.seedSubscription(t, "active", "sub_test_recurring")

	svc := &Service{pool: fx.pool}
	payment := &mollie.Payment{
		ID:             fx.molliePaymentID,
		Status:         "failed",
		SubscriptionID: fx.mollieSubscriptionID,
	}
	if err := svc.markPaymentFailure(context.Background(), payment); err != nil {
		t.Fatalf("markPaymentFailure: %v", err)
	}

	fx.assertInvoiceStatus(t, "failed")
	fx.assertSubscription(t, "past_due", false /*canceled_at*/, true /*past_due_since*/)
}

// TestMarkPaymentFailure_LiveSubGuardBlocksDelayedRedelivery —
// case (g). Locks in the #467 fix: the abandonment-branch UPDATE
// on subscriptions is guarded by AND status = 'incomplete', so a
// delayed webhook redelivery for a payment that already succeeded
// (subscription is now 'active') doesn't cancel the live sub.
//
// This is the exact regression the guard exists to prevent — if a
// future refactor loses the guard, this test fails.
func TestMarkPaymentFailure_LiveSubGuardBlocksDelayedRedelivery(t *testing.T) {
	fx := setupPaymentFailureFixture(t)
	fx.seedInvoice(t)
	fx.seedSubscription(t, "active", "") // already-live sub, no mollie_subscription_id

	svc := &Service{pool: fx.pool}
	payment := &mollie.Payment{
		ID:       fx.molliePaymentID,
		Status:   "canceled",
		Metadata: map[string]string{"subscription_id": fx.subscriptionID},
	}
	if err := svc.markPaymentFailure(context.Background(), payment); err != nil {
		t.Fatalf("markPaymentFailure: %v", err)
	}

	// Invoice still gets marked failed (that's the audit trail).
	fx.assertInvoiceStatus(t, "failed")
	// But the live sub must be untouched — status stays 'active',
	// canceled_at stays NULL. If this fails, someone removed the
	// AND status = 'incomplete' guard from webhook.go.
	fx.assertSubscription(t, "active", false /*canceled_at*/, false /*past_due_since*/)
}
