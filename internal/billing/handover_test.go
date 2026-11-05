package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eurobase/euroback/internal/billing/mollie"
	"github.com/jackc/pgx/v5/pgxpool"
)

// fakeMollie is a minimal Mollie API: it records requests and serves the
// payments a test registers.
type fakeMollie struct {
	mu            sync.Mutex
	payments      map[string]map[string]any // GET /payments/{id}
	createdPay    []map[string]any          // POST /payments bodies
	createdSubs   []map[string]any          // POST …/subscriptions bodies
	canceledSubs  []string                  // DELETE …/subscriptions/{id}
	refunded      []string                  // POST /payments/{id}/refunds
	nextPaymentID int
	failCancel    bool   // DELETE …/subscriptions/{id} answers 500
	onCreateSub   func() // runs while a subscription is being created
}

func newFakeMollie(t *testing.T) (*fakeMollie, *mollie.Client) {
	f := &fakeMollie{payments: map[string]map[string]any{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	c := mollie.NewClient(mollie.Config{APIKey: "test_fake", Env: mollie.EnvTest, BaseURL: srv.URL})
	return f, c
}

func (f *fakeMollie) setPayment(id string, p map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p["id"] = id
	f.payments[id] = p
}

func (f *fakeMollie) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body := map[string]any{}
	if b, _ := io.ReadAll(r.Body); len(b) > 0 {
		_ = json.Unmarshal(b, &body)
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	reply := func(v any) { w.Header().Set("Content-Type", "application/json"); _ = json.NewEncoder(w).Encode(v) }
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/customers":
		reply(map[string]any{"id": "cst_new"})
	case r.Method == http.MethodPost && r.URL.Path == "/payments":
		f.createdPay = append(f.createdPay, body)
		f.nextPaymentID++
		reply(map[string]any{
			"id":     fmt.Sprintf("tr_created_%d", f.nextPaymentID),
			"_links": map[string]any{"checkout": map[string]any{"href": "https://mollie.test/checkout"}},
		})
	case r.Method == http.MethodGet && len(parts) == 2 && parts[0] == "payments":
		p, ok := f.payments[parts[1]]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			reply(map[string]any{"status": 404})
			return
		}
		reply(p)
	case r.Method == http.MethodPost && len(parts) == 3 && parts[2] == "refunds":
		f.refunded = append(f.refunded, parts[1])
		reply(map[string]any{"id": "re_1"})
	case r.Method == http.MethodPost && len(parts) == 3 && parts[2] == "subscriptions":
		if f.onCreateSub != nil {
			f.onCreateSub()
		}
		f.createdSubs = append(f.createdSubs, body)
		start, _ := body["startDate"].(string)
		reply(map[string]any{"id": "sub_new", "status": "active", "startDate": start, "nextPaymentDate": start})
	case r.Method == http.MethodDelete && len(parts) == 4 && f.failCancel:
		w.WriteHeader(http.StatusInternalServerError)
		reply(map[string]any{"status": 500})
	case r.Method == http.MethodDelete && len(parts) == 4:
		f.canceledSubs = append(f.canceledSubs, parts[3])
		reply(map[string]any{"id": parts[3], "status": "canceled"})
	case r.Method == http.MethodGet && len(parts) == 4:
		reply(map[string]any{"id": parts[3], "status": "active", "nextPaymentDate": time.Now().AddDate(0, 1, 0).Format("2006-01-02")})
	default:
		w.WriteHeader(http.StatusNotFound)
		reply(map[string]any{"status": 404})
	}
}

func handoverTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping DB integration test in -short mode")
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
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
	t.Cleanup(pool.Close)
	return pool
}

type handoverFixture struct {
	pool                *pgxpool.Pool
	oldOwner, newOwner  string
	projectID, oldSubID string
	paidUntil           time.Time
}

// seedHandover: a Pro project that has just been transferred — the new
// owner owns it, the previous owner's subscription is still live.
func seedHandover(t *testing.T, pool *pgxpool.Pool, oldStatus string) *handoverFixture {
	t.Helper()
	ctx := context.Background()
	u := fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	fx := &handoverFixture{pool: pool, paidUntil: time.Now().UTC().AddDate(0, 0, 10).Truncate(24 * time.Hour)}
	if err := pool.QueryRow(ctx,
		`INSERT INTO platform_users (email, mollie_customer_id) VALUES ($1, 'cst_old') RETURNING id`,
		"ho-old-"+u+"@eurobase.app").Scan(&fx.oldOwner); err != nil {
		t.Skipf("seed: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO platform_users (email) VALUES ($1) RETURNING id`,
		"ho-new-"+u+"@eurobase.app").Scan(&fx.newOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO public.billing_profiles
		    (platform_user_id, entity_type, legal_name, street_address, postal_code, city, country)
		 VALUES ($1, 'individual', 'New Owner', 'Street 1', '10115', 'Berlin', 'DE')`,
		fx.newOwner); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO projects (owner_id, name, slug, schema_name, s3_bucket, region, plan, status)
		 VALUES ($1, 'Handover', $2, $3, $4, 'fr-par', 'pro', 'active') RETURNING id`,
		fx.newOwner, "test-ho-"+u, "tenant_test_ho_"+u, "eurobase-test-ho-"+u,
	).Scan(&fx.projectID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO public.subscriptions
		    (project_id, mollie_customer_id, mollie_subscription_id, plan, price_cents,
		     currency, billing_interval, status, started_at, next_charge_at)
		 VALUES ($1, 'cst_old', NULLIF($2, ''), 'pro', 2500, 'EUR', '1 month', $3, now() - interval '20 days', $4)
		 RETURNING id`,
		// An open checkout has no Mollie subscription yet.
		fx.projectID, map[bool]string{true: "", false: "sub_old_" + u}[oldStatus == "incomplete"], oldStatus, fx.paidUntil,
	).Scan(&fx.oldSubID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM public.invoices WHERE project_id = $1`, fx.projectID)
		_, _ = pool.Exec(ctx, `UPDATE public.subscriptions SET predecessor_id = NULL WHERE project_id = $1`, fx.projectID)
		_, _ = pool.Exec(ctx, `DELETE FROM public.subscriptions WHERE project_id = $1`, fx.projectID)
		_, _ = pool.Exec(ctx, `DELETE FROM public.projects WHERE id = $1`, fx.projectID)
		_, _ = pool.Exec(ctx, `DELETE FROM public.billing_profiles WHERE platform_user_id = $1`, fx.newOwner)
		_, _ = pool.Exec(ctx, `DELETE FROM platform_users WHERE id = ANY($1)`, []string{fx.oldOwner, fx.newOwner})
	})
	return fx
}

func (fx *handoverFixture) status(t *testing.T, subID string) string {
	t.Helper()
	var s string
	if err := fx.pool.QueryRow(context.Background(), `SELECT status FROM public.subscriptions WHERE id = $1`, subID).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func (fx *handoverFixture) plan(t *testing.T) string {
	t.Helper()
	var p string
	if err := fx.pool.QueryRow(context.Background(), `SELECT plan FROM public.projects WHERE id = $1`, fx.projectID).Scan(&p); err != nil {
		t.Fatal(err)
	}
	return p
}

// The full no-gap, no-double-charge handover: transfer → new owner's
// zero-amount checkout → Mollie confirms → the period ends → the new
// subscription takes over, the project stays Pro, a late charge on the
// old subscription is refunded, and the new owner's charges are billed to
// them.
func TestHandover_NoGapNoDoubleCharge(t *testing.T) {
	pool := handoverTestDB(t)
	ctx := context.Background()
	fake, client := newFakeMollie(t)
	svc := NewService(pool, client, Config{ConsoleBaseURL: "https://console.test", WebhookBaseURL: "https://api.test"}, true)
	fx := seedHandover(t, pool, "active")

	// 1. Transfer accepted → handover.
	res, err := svc.HandOverOnTransfer(ctx, fx.projectID)
	if err != nil {
		t.Fatalf("handover: %v", err)
	}
	if !res.HandedOver || res.PaidUntil == nil || !res.PaidUntil.Equal(fx.paidUntil) || res.MollieCancelFailed {
		t.Fatalf("handover result = %+v, want handed over until %v", res, fx.paidUntil)
	}
	if len(fake.canceledSubs) != 1 {
		t.Fatalf("mollie cancels = %v, want the old subscription", fake.canceledSubs)
	}
	if got := fx.status(t, fx.oldSubID); got != "active" {
		t.Fatalf("old subscription status = %s, want active until the period ends", got)
	}

	// The new owner can't cancel the previous owner's subscription.
	if _, err := svc.CancelSubscription(ctx, fx.newOwner, fx.oldSubID, CancelModeEndOfPeriod); !errors.Is(err, ErrSubscriptionNotFound) {
		t.Fatalf("new owner cancelling the old subscription: err = %v, want ErrSubscriptionNotFound", err)
	}

	// 2. New owner subscribes right away: zero-amount, card/PayPal only,
	// no invoice, scheduled to start when the paid period ends.
	co, err := svc.CreateCheckout(ctx, fx.newOwner, fx.projectID, "pro")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if co.StartsAt == nil || !co.StartsAt.Equal(fx.paidUntil) {
		t.Fatalf("starts_at = %v, want %v", co.StartsAt, fx.paidUntil)
	}
	pay := fake.createdPay[len(fake.createdPay)-1]
	if amt := pay["amount"].(map[string]any)["value"]; amt != "0.00" {
		t.Errorf("first payment amount = %v, want 0.00", amt)
	}
	if m := fmt.Sprint(pay["method"]); m != "[creditcard paypal]" {
		t.Errorf("first payment method = %s, want [creditcard paypal]", m)
	}
	if pay["sequenceType"] != "first" {
		t.Errorf("sequenceType = %v, want first", pay["sequenceType"])
	}
	if got := fx.status(t, co.SubscriptionID); got != "scheduled" {
		t.Fatalf("new subscription status = %s, want scheduled", got)
	}
	var invoices int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM public.invoices WHERE project_id = $1`, fx.projectID).Scan(&invoices)
	if invoices != 0 {
		t.Errorf("invoices after the zero-amount checkout = %d, want 0", invoices)
	}

	// 3. Mollie confirms the mandate payment.
	fake.setPayment("tr_mandate", map[string]any{
		"status": "paid", "sequenceType": "first", "customerId": "cst_new", "mandateId": "mdt_1",
		"amount": map[string]any{"value": "0.00", "currency": "EUR"}, "createdAt": time.Now().UTC(),
		"metadata": map[string]any{"subscription_id": co.SubscriptionID, "project_id": fx.projectID, "plan_code": "pro"},
	})
	if err := svc.ProcessMollieWebhook(ctx, "tr_mandate"); err != nil {
		t.Fatalf("mandate webhook: %v", err)
	}
	if len(fake.createdSubs) != 1 {
		t.Fatalf("mollie subscriptions created = %d, want 1", len(fake.createdSubs))
	}
	sub := fake.createdSubs[0]
	if sub["startDate"] != fx.paidUntil.Format("2006-01-02") || sub["mandateId"] != "mdt_1" {
		t.Errorf("mollie subscription = %v, want startDate %s on mdt_1", sub, fx.paidUntil.Format("2006-01-02"))
	}
	if got := fx.status(t, co.SubscriptionID); got != "scheduled" {
		t.Fatalf("after confirmation status = %s, want scheduled until the period ends", got)
	}
	// A second checkout now is refused: one is already set up.
	if _, err := svc.CreateCheckout(ctx, fx.newOwner, fx.projectID, "pro"); !errors.Is(err, ErrAlreadyScheduled) {
		t.Errorf("second checkout: err = %v, want ErrAlreadyScheduled", err)
	}

	// 4. The paid period ends → the sweep hands over instead of downgrading.
	if _, err := pool.Exec(ctx,
		`UPDATE public.subscriptions SET next_charge_at = now() - interval '1 hour' WHERE id = $1`, fx.oldSubID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE public.subscriptions SET starts_at = now() - interval '1 hour' WHERE id = $1`, co.SubscriptionID); err != nil {
		t.Fatal(err)
	}
	NewDowngradeService(pool, client, nil, "").RunSweep(ctx)
	if got := fx.status(t, fx.oldSubID); got != "expired" {
		t.Errorf("old subscription after period end = %s, want expired", got)
	}
	if got := fx.status(t, co.SubscriptionID); got != "active" {
		t.Errorf("new subscription after period end = %s, want active", got)
	}
	if got := fx.plan(t); got != "pro" {
		t.Errorf("project plan after period end = %s, want pro (no Free gap)", got)
	}

	// 5. A charge Mollie still made on the old subscription is refunded
	// and not invoiced.
	var oldMollieSub string
	_ = pool.QueryRow(ctx, `SELECT mollie_subscription_id FROM public.subscriptions WHERE id = $1`, fx.oldSubID).Scan(&oldMollieSub)
	fake.setPayment("tr_late_old", map[string]any{
		"status": "paid", "sequenceType": "recurring", "customerId": "cst_old", "subscriptionId": oldMollieSub,
		"amount": map[string]any{"value": "25.00", "currency": "EUR"}, "createdAt": time.Now().UTC(),
	})
	if err := svc.ProcessMollieWebhook(ctx, "tr_late_old"); err != nil {
		t.Fatalf("late old charge webhook: %v", err)
	}
	if len(fake.refunded) != 1 || fake.refunded[0] != "tr_late_old" {
		t.Errorf("refunds = %v, want the late charge on the old subscription", fake.refunded)
	}
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM public.invoices WHERE mollie_payment_id = 'tr_late_old'`).Scan(&invoices)
	if invoices != 0 {
		t.Errorf("late old charge was invoiced")
	}

	// 6. The new owner's first charge is invoiced to the new owner.
	fake.setPayment("tr_new_first", map[string]any{
		"status": "paid", "sequenceType": "recurring", "customerId": "cst_new", "subscriptionId": "sub_new",
		"amount": map[string]any{"value": "25.00", "currency": "EUR"}, "createdAt": time.Now().UTC(),
	})
	if err := svc.ProcessMollieWebhook(ctx, "tr_new_first"); err != nil {
		t.Fatalf("new first charge webhook: %v", err)
	}
	var billedTo string
	if err := pool.QueryRow(ctx, `SELECT billed_to_user_id FROM public.invoices WHERE mollie_payment_id = 'tr_new_first'`).Scan(&billedTo); err != nil {
		t.Fatalf("new owner's invoice: %v", err)
	}
	if billedTo != fx.newOwner {
		t.Errorf("invoice billed to %s, want the new owner %s", billedTo, fx.newOwner)
	}
}

// Without a confirmed successor the period-end sweep still drops the
// project to Free, as before.
func TestHandover_NoSuccessorDowngrades(t *testing.T) {
	pool := handoverTestDB(t)
	ctx := context.Background()
	fake, client := newFakeMollie(t)
	svc := NewService(pool, client, Config{}, true)
	fx := seedHandover(t, pool, "active")
	if _, err := svc.HandOverOnTransfer(ctx, fx.projectID); err != nil {
		t.Fatal(err)
	}
	// An abandoned zero-amount checkout doesn't count as a successor.
	if _, err := svc.CreateCheckout(ctx, fx.newOwner, fx.projectID, "pro"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE public.subscriptions SET next_charge_at = now() - interval '1 hour' WHERE id = $1`, fx.oldSubID); err != nil {
		t.Fatal(err)
	}
	NewDowngradeService(pool, client, nil, "").RunSweep(ctx)
	if got := fx.plan(t); got != "free" {
		t.Errorf("plan = %s, want free", got)
	}
	_ = fake
}

// A checkout the previous owner left open is closed by the handover; if
// its payment still completes it is refunded and starts nothing.
func TestHandover_OpenCheckoutOfPreviousOwnerRefunded(t *testing.T) {
	pool := handoverTestDB(t)
	ctx := context.Background()
	fake, client := newFakeMollie(t)
	svc := NewService(pool, client, Config{}, true)
	fx := seedHandover(t, pool, "incomplete")

	res, err := svc.HandOverOnTransfer(ctx, fx.projectID)
	if err != nil {
		t.Fatal(err)
	}
	if res.HandedOver {
		t.Errorf("an open checkout was reported as a paid subscription")
	}
	if got := fx.status(t, fx.oldSubID); got != "canceled" {
		t.Fatalf("open checkout status = %s, want canceled", got)
	}

	fake.setPayment("tr_old_checkout", map[string]any{
		"status": "paid", "sequenceType": "first", "customerId": "cst_old", "mandateId": "mdt_old",
		"amount": map[string]any{"value": "25.00", "currency": "EUR"}, "createdAt": time.Now().UTC(),
		"metadata": map[string]any{"subscription_id": fx.oldSubID, "project_id": fx.projectID, "plan_code": "pro"},
	})
	if err := svc.ProcessMollieWebhook(ctx, "tr_old_checkout"); err != nil {
		t.Fatalf("webhook: %v", err)
	}
	if len(fake.createdSubs) != 0 {
		t.Errorf("a subscription was started on the previous owner's mandate")
	}
	if len(fake.refunded) != 1 {
		t.Errorf("refunds = %v, want the late first payment refunded", fake.refunded)
	}
}

// A project whose last payment failed can't be transferred.
func TestHandover_PastDueNotTransferable(t *testing.T) {
	pool := handoverTestDB(t)
	_, client := newFakeMollie(t)
	svc := NewService(pool, client, Config{}, true)
	fx := seedHandover(t, pool, "past_due")
	if err := svc.CheckTransferable(context.Background(), fx.projectID); !errors.Is(err, ErrPaymentOverdue) {
		t.Errorf("err = %v, want ErrPaymentOverdue", err)
	}
}

// Review round 2 on #855.

func TestHandover_BetaGrantStatusStillAllowed(t *testing.T) {
	pool := handoverTestDB(t)
	fx := seedHandover(t, pool, "active")
	if _, err := pool.Exec(context.Background(),
		`UPDATE public.subscriptions SET status = 'beta_grant' WHERE id = $1`, fx.oldSubID); err != nil {
		t.Fatalf("beta_grant rejected by the status check: %v", err)
	}
}

// Mollie unreachable during the handover: the database side still
// completes, so a later charge is refunded.
func TestHandover_MollieCancelFails(t *testing.T) {
	pool := handoverTestDB(t)
	ctx := context.Background()
	fake, client := newFakeMollie(t)
	fake.failCancel = true
	svc := NewService(pool, client, Config{}, true)
	fx := seedHandover(t, pool, "active")

	res, err := svc.HandOverOnTransfer(ctx, fx.projectID)
	if err != nil {
		t.Fatalf("handover: %v", err)
	}
	if !res.HandedOver || !res.MollieCancelFailed {
		t.Fatalf("result = %+v, want handed over with MollieCancelFailed", res)
	}
	var canceled bool
	_ = pool.QueryRow(ctx, `SELECT canceled_at IS NOT NULL FROM public.subscriptions WHERE id = $1`, fx.oldSubID).Scan(&canceled)
	if !canceled {
		t.Fatal("canceled_at not stamped when the Mollie cancel failed")
	}
	var mollieSub string
	_ = pool.QueryRow(ctx, `SELECT mollie_subscription_id FROM public.subscriptions WHERE id = $1`, fx.oldSubID).Scan(&mollieSub)
	fake.setPayment("tr_after", map[string]any{
		"status": "paid", "sequenceType": "recurring", "customerId": "cst_old", "subscriptionId": mollieSub,
		"amount": map[string]any{"value": "25.00", "currency": "EUR"}, "createdAt": time.Now().UTC().Add(time.Minute),
	})
	if err := svc.ProcessMollieWebhook(ctx, "tr_after"); err != nil {
		t.Fatal(err)
	}
	if len(fake.refunded) != 1 {
		t.Errorf("refunds = %v, want the charge after the failed cancel refunded", fake.refunded)
	}
}

// With no known period end, the handover derives one from the last paid
// charge instead of leaving the project on Pro forever.
func TestHandover_NoNextChargeDerived(t *testing.T) {
	pool := handoverTestDB(t)
	ctx := context.Background()
	_, client := newFakeMollie(t)
	svc := NewService(pool, client, Config{}, true)
	fx := seedHandover(t, pool, "active")
	paid := time.Now().UTC().Add(-5 * 24 * time.Hour).Truncate(time.Second)
	if _, err := pool.Exec(ctx, `UPDATE public.subscriptions SET next_charge_at = NULL WHERE id = $1`, fx.oldSubID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO public.invoices (project_id, subscription_id, mollie_payment_id, amount_cents, currency, status, paid_at, billed_to_user_id)
		 VALUES ($1, $2, $3, 2500, 'EUR', 'paid', $4, $5)`,
		fx.projectID, fx.oldSubID, "tr_paid_"+fx.oldSubID, paid, fx.oldOwner); err != nil {
		t.Fatal(err)
	}
	res, err := svc.HandOverOnTransfer(ctx, fx.projectID)
	if err != nil {
		t.Fatal(err)
	}
	want := addMonthClamped(paid)
	if res.PaidUntil == nil || !res.PaidUntil.Equal(want) {
		t.Errorf("paid until = %v, want %v", res.PaidUntil, want)
	}
}

// A confirmed waiting subscription blocks a second checkout even when
// nothing is live any more (no double charge), and the summary shows it.
func TestHandover_WaitingSubscriptionBlocksSecondCheckout(t *testing.T) {
	pool := handoverTestDB(t)
	ctx := context.Background()
	fake, client := newFakeMollie(t)
	svc := NewService(pool, client, Config{}, true)
	fx := seedHandover(t, pool, "active")
	if _, err := svc.HandOverOnTransfer(ctx, fx.projectID); err != nil {
		t.Fatal(err)
	}
	co, err := svc.CreateCheckout(ctx, fx.newOwner, fx.projectID, "pro")
	if err != nil {
		t.Fatal(err)
	}
	fake.setPayment("tr_m", map[string]any{
		"status": "paid", "sequenceType": "first", "customerId": "cst_new", "mandateId": "mdt_1",
		"amount": map[string]any{"value": "0.00", "currency": "EUR"}, "createdAt": time.Now().UTC(),
		"metadata": map[string]any{"subscription_id": co.SubscriptionID, "project_id": fx.projectID, "plan_code": "pro"},
	})
	if err := svc.ProcessMollieWebhook(ctx, "tr_m"); err != nil {
		t.Fatal(err)
	}
	// The subscription it was to follow ends early (e.g. by ops).
	if _, err := pool.Exec(ctx, `UPDATE public.subscriptions SET status = 'canceled' WHERE id = $1`, fx.oldSubID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE public.projects SET plan = 'free' WHERE id = $1`, fx.projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateCheckout(ctx, fx.newOwner, fx.projectID, "pro"); !errors.Is(err, ErrAlreadyScheduled) {
		t.Errorf("checkout with a waiting subscription: err = %v, want ErrAlreadyScheduled", err)
	}
	// A repeated first-payment webhook (e.g. after a refund on it) for a
	// subscription that has since ended refunds nothing.
	if _, err := pool.Exec(ctx, `UPDATE public.subscriptions SET status = 'canceled' WHERE id = $1`, co.SubscriptionID); err != nil {
		t.Fatal(err)
	}
	if err := svc.ProcessMollieWebhook(ctx, "tr_m"); err != nil {
		t.Fatal(err)
	}
	if len(fake.refunded) != 0 {
		t.Errorf("refunds = %v, want none for an already-processed first payment", fake.refunded)
	}
}

// Mollie charges the scheduled subscription before starts_at (its date is
// a day in Amsterdam): the charge promotes it; a failed one is tracked.
func TestHandover_ChargeBeforeStartsAtPromotes(t *testing.T) {
	for _, outcome := range []string{"paid", "failed"} {
		t.Run(outcome, func(t *testing.T) {
			pool := handoverTestDB(t)
			ctx := context.Background()
			fake, client := newFakeMollie(t)
			svc := NewService(pool, client, Config{}, true)
			fx := seedHandover(t, pool, "active")
			if _, err := svc.HandOverOnTransfer(ctx, fx.projectID); err != nil {
				t.Fatal(err)
			}
			co, err := svc.CreateCheckout(ctx, fx.newOwner, fx.projectID, "pro")
			if err != nil {
				t.Fatal(err)
			}
			fake.setPayment("tr_m", map[string]any{
				"status": "paid", "sequenceType": "first", "customerId": "cst_new", "mandateId": "mdt_1",
				"amount": map[string]any{"value": "0.00", "currency": "EUR"}, "createdAt": time.Now().UTC(),
				"metadata": map[string]any{"subscription_id": co.SubscriptionID, "project_id": fx.projectID, "plan_code": "pro"},
			})
			if err := svc.ProcessMollieWebhook(ctx, "tr_m"); err != nil {
				t.Fatal(err)
			}
			fake.setPayment("tr_c", map[string]any{
				"status": outcome, "sequenceType": "recurring", "customerId": "cst_new", "subscriptionId": "sub_new",
				"amount": map[string]any{"value": "25.00", "currency": "EUR"}, "createdAt": time.Now().UTC(),
			})
			if err := svc.ProcessMollieWebhook(ctx, "tr_c"); err != nil {
				t.Fatal(err)
			}
			want := map[string]string{"paid": "active", "failed": "past_due"}[outcome]
			if got := fx.status(t, co.SubscriptionID); got != want {
				t.Errorf("status after a %s charge before starts_at = %s, want %s", outcome, got, want)
			}
			if got := fx.status(t, fx.oldSubID); got != "expired" {
				t.Errorf("old subscription = %s, want expired", got)
			}
		})
	}
}

// The period-end sweep selected the old subscription, then a webhook
// promoted the successor: the sweep must not drop the project to Free.
func TestHandover_SweepRaceKeepsPlan(t *testing.T) {
	pool := handoverTestDB(t)
	ctx := context.Background()
	fake, client := newFakeMollie(t)
	svc := NewService(pool, client, Config{}, true)
	ds := NewDowngradeService(pool, client, nil, "")
	fx := seedHandover(t, pool, "active")
	if _, err := svc.HandOverOnTransfer(ctx, fx.projectID); err != nil {
		t.Fatal(err)
	}
	co, err := svc.CreateCheckout(ctx, fx.newOwner, fx.projectID, "pro")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE public.subscriptions SET next_charge_at = now() - interval '1 hour' WHERE id = $1`, fx.oldSubID); err != nil {
		t.Fatal(err)
	}
	cands, err := ds.findEndOfPeriodElapsed(ctx) // successor not confirmed yet → selected
	if err != nil {
		t.Fatal(err)
	}
	var mine *downgradeCandidate
	for i := range cands {
		if cands[i].ProjectID == fx.projectID {
			mine = &cands[i]
		}
	}
	if mine == nil {
		t.Fatal("old subscription not selected by the end-of-period sweep")
	}
	if _, err := pool.Exec(ctx, `UPDATE public.subscriptions SET starts_at = now() - interval '1 hour' WHERE id = $1`, co.SubscriptionID); err != nil {
		t.Fatal(err)
	}
	fake.setPayment("tr_m", map[string]any{
		"status": "paid", "sequenceType": "first", "customerId": "cst_new", "mandateId": "mdt_1",
		"amount": map[string]any{"value": "0.00", "currency": "EUR"}, "createdAt": time.Now().UTC(),
		"metadata": map[string]any{"subscription_id": co.SubscriptionID, "project_id": fx.projectID, "plan_code": "pro"},
	})
	if err := svc.ProcessMollieWebhook(ctx, "tr_m"); err != nil { // late confirm → promotes now
		t.Fatal(err)
	}
	ds.downgradeOne(ctx, *mine, "end_of_period_cancel")
	if got := fx.plan(t); got != "pro" {
		t.Errorf("plan = %s, want pro (successor active)", got)
	}
}

// A checkout of the previous owner that slipped in during the transfer
// (inserted after the handover closed open checkouts): its payment is
// refunded even though the new owner has no Mollie customer yet.
func TestHandover_PreviousOwnerPaymentRefusedWhenNewOwnerHasNoCustomer(t *testing.T) {
	pool := handoverTestDB(t)
	ctx := context.Background()
	fake, client := newFakeMollie(t)
	svc := NewService(pool, client, Config{}, true)
	fx := seedHandover(t, pool, "active")
	if _, err := svc.HandOverOnTransfer(ctx, fx.projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE public.subscriptions SET status = 'expired' WHERE id = $1`, fx.oldSubID); err != nil {
		t.Fatal(err)
	}
	var lateID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO public.subscriptions (project_id, mollie_customer_id, plan, price_cents, currency, billing_interval, status)
		 VALUES ($1, 'cst_old', 'pro', 2500, 'EUR', '1 month', 'incomplete') RETURNING id`,
		fx.projectID).Scan(&lateID); err != nil {
		t.Fatal(err)
	}
	fake.setPayment("tr_late_first", map[string]any{
		"status": "paid", "sequenceType": "first", "customerId": "cst_old", "mandateId": "mdt_old",
		"amount": map[string]any{"value": "25.00", "currency": "EUR"}, "createdAt": time.Now().UTC(),
		"metadata": map[string]any{"subscription_id": lateID, "project_id": fx.projectID, "plan_code": "pro"},
	})
	if err := svc.ProcessMollieWebhook(ctx, "tr_late_first"); err != nil {
		t.Fatal(err)
	}
	if len(fake.createdSubs) != 0 || len(fake.refunded) != 1 {
		t.Errorf("created subs = %d, refunds = %v; want none created and the payment refunded", len(fake.createdSubs), fake.refunded)
	}
}

// Review round 3 on #855.

// The checkout is closed while the webhook creates the Mollie subscription:
// the subscription is cancelled again, the payment refunded, and neither
// the invoice nor the plan says otherwise.
func TestActivation_CheckoutClosedMeanwhile(t *testing.T) {
	pool := handoverTestDB(t)
	ctx := context.Background()
	fake, client := newFakeMollie(t)
	svc := NewService(pool, client, Config{}, true)
	fx := seedHandover(t, pool, "expired") // nothing live: a normal checkout
	if _, err := pool.Exec(ctx, `UPDATE public.projects SET plan = 'free' WHERE id = $1`, fx.projectID); err != nil {
		t.Fatal(err)
	}
	co, err := svc.CreateCheckout(ctx, fx.newOwner, fx.projectID, "pro")
	if err != nil {
		t.Fatal(err)
	}
	if co.StartsAt != nil {
		t.Fatal("normal checkout came back scheduled")
	}
	var payID string
	_ = pool.QueryRow(ctx, `SELECT mollie_payment_id FROM public.invoices WHERE subscription_id = $1`, co.SubscriptionID).Scan(&payID)
	fake.onCreateSub = func() {
		_, _ = pool.Exec(ctx, `UPDATE public.subscriptions SET status = 'canceled' WHERE id = $1`, co.SubscriptionID)
	}
	fake.setPayment(payID, map[string]any{
		"status": "paid", "sequenceType": "first", "customerId": "cst_new", "mandateId": "mdt_1",
		"amount": map[string]any{"value": "25.00", "currency": "EUR"}, "createdAt": time.Now().UTC(),
		"metadata": map[string]any{"subscription_id": co.SubscriptionID, "project_id": fx.projectID, "plan_code": "pro"},
	})
	if err := svc.ProcessMollieWebhook(ctx, payID); err != nil {
		t.Fatal(err)
	}
	if len(fake.canceledSubs) != 1 || fake.canceledSubs[0] != "sub_new" {
		t.Errorf("mollie cancels = %v, want the just-created subscription", fake.canceledSubs)
	}
	if len(fake.refunded) != 1 {
		t.Errorf("refunds = %v, want the first payment refunded", fake.refunded)
	}
	if got := fx.plan(t); got != "free" {
		t.Errorf("plan = %s, want free", got)
	}
	var invStatus string
	_ = pool.QueryRow(ctx, `SELECT status FROM public.invoices WHERE mollie_payment_id = $1`, payID).Scan(&invStatus)
	if invStatus != "refunded" {
		t.Errorf("invoice status = %s, want refunded", invStatus)
	}
}

// A €0 checkout confirmed after the owner already subscribed normally
// starts nothing (it would charge twice).
func TestActivation_ScheduledSupersededByNormalSubscription(t *testing.T) {
	pool := handoverTestDB(t)
	ctx := context.Background()
	fake, client := newFakeMollie(t)
	svc := NewService(pool, client, Config{}, true)
	fx := seedHandover(t, pool, "active")
	if _, err := svc.HandOverOnTransfer(ctx, fx.projectID); err != nil {
		t.Fatal(err)
	}
	co, err := svc.CreateCheckout(ctx, fx.newOwner, fx.projectID, "pro")
	if err != nil {
		t.Fatal(err)
	}
	// Period ended, project dropped to Free, owner subscribed normally
	// (a new live subscription, not cancelled).
	if _, err := pool.Exec(ctx, `UPDATE public.subscriptions SET status = 'expired' WHERE id = $1`, fx.oldSubID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO public.subscriptions (project_id, mollie_customer_id, mollie_subscription_id, plan, price_cents, currency, billing_interval, status)
		 VALUES ($1, 'cst_new', 'sub_normal', 'pro', 2500, 'EUR', '1 month', 'active')`, fx.projectID); err != nil {
		t.Fatal(err)
	}
	fake.setPayment("tr_m", map[string]any{
		"status": "paid", "sequenceType": "first", "customerId": "cst_new", "mandateId": "mdt_1",
		"amount": map[string]any{"value": "0.00", "currency": "EUR"}, "createdAt": time.Now().UTC(),
		"metadata": map[string]any{"subscription_id": co.SubscriptionID, "project_id": fx.projectID, "plan_code": "pro"},
	})
	if err := svc.ProcessMollieWebhook(ctx, "tr_m"); err != nil {
		t.Fatal(err)
	}
	if len(fake.createdSubs) != 0 {
		t.Errorf("a second Mollie subscription was started")
	}
	if got := fx.status(t, co.SubscriptionID); got != "canceled" {
		t.Errorf("scheduled subscription = %s, want canceled", got)
	}
}

// promoteScheduled never leaves a successor charging next to an
// uncancelled live subscription: it cancels the successor.
func TestPromote_SupersededSuccessorCancelled(t *testing.T) {
	pool := handoverTestDB(t)
	ctx := context.Background()
	fake, client := newFakeMollie(t)
	fx := seedHandover(t, pool, "active") // live and NOT cancelled
	var succID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO public.subscriptions (project_id, mollie_customer_id, mollie_subscription_id, plan, price_cents, currency, billing_interval, status, starts_at)
		 VALUES ($1, 'cst_new', 'sub_succ', 'pro', 2500, 'EUR', '1 month', 'scheduled', now() - interval '1 hour') RETURNING id`,
		fx.projectID).Scan(&succID); err != nil {
		t.Fatal(err)
	}
	promoted, err := promoteScheduled(ctx, pool, client, fx.projectID, true)
	if err != nil || promoted {
		t.Fatalf("promoted = %v, err = %v; want false, nil", promoted, err)
	}
	if got := fx.status(t, succID); got != "canceled" {
		t.Errorf("successor = %s, want canceled", got)
	}
	if got := fx.status(t, fx.oldSubID); got != "active" {
		t.Errorf("live subscription = %s, want untouched", got)
	}
	if len(fake.canceledSubs) != 1 || fake.canceledSubs[0] != "sub_succ" {
		t.Errorf("mollie cancels = %v, want the successor", fake.canceledSubs)
	}
}
