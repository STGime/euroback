package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eurobase/euroback/internal/billing/mollie"
)

func TestMandateView(t *testing.T) {
	next := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)

	card := &mollie.Mandate{Method: "creditcard", Status: "valid", Details: mollie.MandateDetails{
		CardLabel: "Visa", CardNumber: "4242", CardExpiryDate: "2026-11-30"}}
	v := mandateView(card, &next)
	if v.CardLast4 != "4242" || v.CardExpiry != "2026-11" || !v.ExpiresBeforeNextCharge {
		t.Errorf("card expiring in November, charge on 1 December: %+v", v)
	}
	card.Details.CardExpiryDate = "2026-12-31"
	if v := mandateView(card, &next); v.ExpiresBeforeNextCharge {
		t.Error("card valid through December flagged as expiring before a 1 December charge")
	}

	sepa := mandateView(&mollie.Mandate{Method: "directdebit", Details: mollie.MandateDetails{
		ConsumerName: "A Person", ConsumerAccount: "NL55INGB0000000000"}}, &next)
	if sepa.Account != "•••• 0000" || strings.Contains(sepa.Account, "INGB") {
		t.Errorf("IBAN not masked: %q", sepa.Account)
	}

	pp := mandateView(&mollie.Mandate{Method: "paypal", Details: mollie.MandateDetails{
		ConsumerName: "A Person", ConsumerAccount: "a@example.com"}}, &next)
	if pp.Account != "a@example.com" {
		t.Errorf("paypal account = %q", pp.Account)
	}
}

func TestAlreadyRefunded(t *testing.T) {
	eur := func(v string) mollie.Amount { return mollie.Amount{Currency: "EUR", Value: v} }
	full := eur("25.00")
	part := eur("10.00")
	for _, tc := range []struct {
		p    mollie.Payment
		want bool
	}{
		{mollie.Payment{Amount: eur("25.00")}, false},
		{mollie.Payment{Amount: eur("25.00"), AmountRefunded: &part}, false},
		{mollie.Payment{Amount: eur("25.00"), AmountRefunded: &full}, true},
		{mollie.Payment{Amount: eur("0.00"), AmountRefunded: &full}, false},
	} {
		if got := alreadyRefunded(&tc.p); got != tc.want {
			t.Errorf("%+v: got %v, want %v", tc.p, got, tc.want)
		}
	}
}

// pmMollie is a fake Mollie for the payment-method flows: mandates,
// one subscription per id with its mandate, PATCH, payment creation and
// refunds.
type pmMollie struct {
	mu        sync.Mutex
	mandates  map[string]map[string]any // mandate id → object
	subs      map[string]string         // subscription id → mandate id
	patches   []string                  // "sub:mandate"
	created   []map[string]any          // POST /payments bodies
	refunded  []string
	subStatus map[string]string
	canceled  []string         // DELETE /customers/…/subscriptions/{id}
	newSubs   []map[string]any // POST /customers/…/subscriptions bodies
}

func newPMMollie(t *testing.T) (*pmMollie, *mollie.Client) {
	f := &pmMollie{mandates: map[string]map[string]any{}, subs: map[string]string{}, subStatus: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, mollie.NewClient(mollie.Config{APIKey: "test_fake", Env: mollie.EnvTest, BaseURL: srv.URL})
}

func (f *pmMollie) addMandate(id, method string, created time.Time) {
	f.mandates[id] = map[string]any{"id": id, "status": "valid", "method": method,
		"createdAt": created.Format(time.RFC3339),
		"details":   map[string]any{"cardLabel": "Visa", "cardNumber": "4242", "cardExpiryDate": "2030-01-31"}}
}

func (f *pmMollie) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body := map[string]any{}
	if b, _ := io.ReadAll(r.Body); len(b) > 0 {
		_ = json.Unmarshal(b, &body)
	}
	reply := func(v any) { w.Header().Set("Content-Type", "application/json"); _ = json.NewEncoder(w).Encode(v) }
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/payments":
		f.created = append(f.created, body)
		reply(map[string]any{"id": "tr_pm_new", "_links": map[string]any{"checkout": map[string]any{"href": "https://mollie.test/pm"}}})
	case len(parts) == 4 && parts[2] == "mandates" && r.Method == http.MethodGet:
		m, ok := f.mandates[parts[3]]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		reply(m)
	case len(parts) == 4 && parts[2] == "subscriptions" && r.Method == http.MethodGet:
		status := f.subStatus[parts[3]]
		if status == "" {
			status = "active"
		}
		reply(map[string]any{"id": parts[3], "status": status, "mandateId": f.subs[parts[3]]})
	case len(parts) == 4 && parts[2] == "subscriptions" && r.Method == http.MethodPatch:
		if f.subStatus[parts[3]] == "canceled" {
			w.WriteHeader(http.StatusUnprocessableEntity)
			reply(map[string]any{"status": 422, "detail": "canceled"})
			return
		}
		m, _ := body["mandateId"].(string)
		f.subs[parts[3]] = m
		f.patches = append(f.patches, parts[3]+":"+m)
		reply(map[string]any{"id": parts[3], "status": "active", "mandateId": m})
	case len(parts) == 4 && parts[2] == "subscriptions" && r.Method == http.MethodDelete:
		if f.subStatus[parts[3]] == "canceled" {
			w.WriteHeader(http.StatusUnprocessableEntity)
			reply(map[string]any{"status": 422, "detail": "already canceled"})
			return
		}
		f.subStatus[parts[3]] = "canceled"
		f.canceled = append(f.canceled, parts[3])
		reply(map[string]any{"id": parts[3], "status": "canceled"})
	case len(parts) == 3 && parts[2] == "subscriptions" && r.Method == http.MethodPost:
		f.newSubs = append(f.newSubs, body)
		id := fmt.Sprintf("sub_new_%d", len(f.newSubs))
		m, _ := body["mandateId"].(string)
		f.subs[id] = m
		reply(map[string]any{"id": id, "status": "active", "mandateId": m, "nextPaymentDate": body["startDate"]})
	case len(parts) == 3 && parts[0] == "payments" && parts[2] == "refunds" && r.Method == http.MethodPost:
		f.refunded = append(f.refunded, parts[1])
		reply(map[string]any{"id": "re_1"})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// seedPayer gives the fixture's owner a Mollie customer and makes the
// subscription theirs (mollie ids, next charge in 10 days).
func seedPayer(t *testing.T, fx *paymentFailureFixture, cust string) {
	t.Helper()
	ctx := context.Background()
	if _, err := fx.pool.Exec(ctx, `UPDATE public.platform_users SET mollie_customer_id = $2 WHERE id = $1`, fx.ownerID, cust); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	if _, err := fx.pool.Exec(ctx,
		`UPDATE public.subscriptions SET mollie_customer_id = $2, next_charge_at = now() + interval '10 days', started_at = now() - interval '20 days' WHERE id = $1`,
		fx.subscriptionID, cust); err != nil {
		t.Fatalf("seed subscription payer: %v", err)
	}
	if _, err := fx.pool.Exec(ctx, `UPDATE public.projects SET status = 'active' WHERE id = $1`, fx.projectID); err != nil {
		t.Fatalf("activate project: %v", err)
	}
}

func TestStartPaymentMethodUpdate(t *testing.T) {
	fx := setupPaymentFailureFixture(t)
	cust := "cst_pm_" + fx.molliePaymentID
	fx.seedSubscription(t, "active", "sub_pm_"+fx.molliePaymentID)
	seedPayer(t, fx, cust)
	f, client := newPMMollie(t)
	svc := NewService(fx.pool, client, Config{ConsoleBaseURL: "https://console.test", WebhookBaseURL: "https://api.test"}, true)
	ctx := context.Background()

	url, err := svc.StartPaymentMethodUpdate(ctx, fx.ownerID, fx.projectID)
	if err != nil || url != "https://mollie.test/pm" {
		t.Fatalf("start: url %q err %v", url, err)
	}
	p := f.created[0]
	amount, _ := p["amount"].(map[string]any)
	meta, _ := p["metadata"].(map[string]any)
	if amount["value"] != "0.00" || p["sequenceType"] != "first" || p["customerId"] != cust ||
		meta["purpose"] != purposePaymentMethodUpdate || meta["subscription_id"] != fx.subscriptionID {
		t.Errorf("payment body = %v", p)
	}
	if methods, _ := p["method"].([]any); len(methods) != 2 {
		t.Errorf("methods = %v, want creditcard + paypal", p["method"])
	}

	// Someone else than the owner.
	if _, err := svc.StartPaymentMethodUpdate(ctx, "00000000-0000-0000-0000-000000000000", fx.projectID); !errors.Is(err, ErrSubscriptionNotFound) {
		t.Errorf("other user: err = %v", err)
	}
	// Cancelled at period end.
	_, _ = fx.pool.Exec(ctx, `UPDATE public.subscriptions SET canceled_at = now() WHERE id = $1`, fx.subscriptionID)
	if _, err := svc.StartPaymentMethodUpdate(ctx, fx.ownerID, fx.projectID); !errors.Is(err, ErrPaymentMethodNotChangeable) {
		t.Errorf("cancelled: err = %v", err)
	}
	// Past due: refused here (PR 3 settles it with a payment).
	_, _ = fx.pool.Exec(ctx, `UPDATE public.subscriptions SET canceled_at = NULL, status = 'past_due', past_due_since = now() WHERE id = $1`, fx.subscriptionID)
	if _, err := svc.StartPaymentMethodUpdate(ctx, fx.ownerID, fx.projectID); !errors.Is(err, ErrPaymentOverdue) {
		t.Errorf("past due: err = %v", err)
	}
	// Paid by someone else (previous owner after a transfer).
	_, _ = fx.pool.Exec(ctx, `UPDATE public.subscriptions SET status = 'active', past_due_since = NULL, mollie_customer_id = 'cst_previous' WHERE id = $1`, fx.subscriptionID)
	if _, err := svc.StartPaymentMethodUpdate(ctx, fx.ownerID, fx.projectID); !errors.Is(err, ErrNotPayer) {
		t.Errorf("not payer: err = %v", err)
	}
	if _, err := svc.GetPaymentMethod(ctx, fx.ownerID, fx.projectID); !errors.Is(err, ErrNotPayer) {
		t.Errorf("not payer can see the payment method: err = %v", err)
	}
}

func TestApplyPaymentMethodUpdate(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name        string
		prepare     func(t *testing.T, fx *paymentFailureFixture, f *pmMollie, subID string)
		wantPatch   bool
		wantMandate string
	}{
		{"moves the subscription to the new mandate", nil, true, "mdt_new"},
		{"cancelled at period end: refused", func(t *testing.T, fx *paymentFailureFixture, _ *pmMollie, _ string) {
			_, _ = fx.pool.Exec(context.Background(), `UPDATE public.subscriptions SET canceled_at = now() WHERE id = $1`, fx.subscriptionID)
		}, false, "mdt_old"},
		{"payer no longer the owner: refused", func(t *testing.T, fx *paymentFailureFixture, _ *pmMollie, _ string) {
			_, _ = fx.pool.Exec(context.Background(), `UPDATE public.platform_users SET mollie_customer_id = 'cst_other' WHERE id = $1`, fx.ownerID)
		}, false, "mdt_old"},
		{"a newer mandate is already in use: kept", func(t *testing.T, _ *paymentFailureFixture, f *pmMollie, subID string) {
			f.addMandate("mdt_newest", "creditcard", now.Add(time.Hour))
			f.subs[subID] = "mdt_newest"
		}, false, "mdt_newest"},
		{"cancelled at Mollie: acknowledged, nothing changes", func(t *testing.T, _ *paymentFailureFixture, f *pmMollie, subID string) {
			f.subStatus[subID] = "canceled"
		}, false, "mdt_old"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := setupPaymentFailureFixture(t)
			cust := "cst_pm_" + fx.molliePaymentID
			subID := "sub_pm_" + fx.molliePaymentID
			fx.seedSubscription(t, "active", subID)
			seedPayer(t, fx, cust)
			f, client := newPMMollie(t)
			f.addMandate("mdt_old", "creditcard", now.Add(-30*24*time.Hour))
			f.addMandate("mdt_new", "paypal", now)
			f.subs[subID] = "mdt_old"
			if tc.prepare != nil {
				tc.prepare(t, fx, f, subID)
			}
			svc := NewService(fx.pool, client, Config{WebhookBaseURL: "https://api.test"}, true)
			payment := &mollie.Payment{
				ID: "tr_pm_" + fx.molliePaymentID, Status: "paid", SequenceType: mollie.SequenceTypeFirst,
				CustomerID: cust, MandateID: "mdt_new", Amount: mollie.Amount{Currency: "EUR", Value: "0.00"},
				Metadata: map[string]string{"purpose": purposePaymentMethodUpdate, "subscription_id": fx.subscriptionID, "project_id": fx.projectID},
			}
			if err := svc.applyPaymentMethodUpdate(context.Background(), payment); err != nil {
				t.Fatalf("apply: %v", err)
			}
			if got := len(f.patches) > 0; got != tc.wantPatch {
				t.Errorf("patched = %v (%v), want %v", got, f.patches, tc.wantPatch)
			}
			if f.subs[subID] != tc.wantMandate {
				t.Errorf("subscription mandate = %q, want %q", f.subs[subID], tc.wantMandate)
			}
			// Redelivery of the same webhook doesn't PATCH again.
			n := len(f.patches)
			_ = svc.applyPaymentMethodUpdate(context.Background(), payment)
			if len(f.patches) != n {
				t.Errorf("redelivery patched again: %v", f.patches)
			}
		})
	}
}

// A charge that lands on a subscription that has ended (e.g. a late
// retry after the downgrade) is refunded, unless it was recorded before.
func TestRecordRecurringPayment_ClosedSubscriptionRefunds(t *testing.T) {
	for _, tc := range []struct {
		name       string
		invoice    string // status of an existing invoice row, "" for none
		wantRefund bool
	}{
		{"new charge on an expired subscription", "", true},
		{"late success of a charge recorded as failed", "failed", true},
		{"redelivery of a charge recorded as paid", "paid", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := setupPaymentFailureFixture(t)
			subID := "sub_closed_" + fx.molliePaymentID
			fx.seedSubscription(t, "expired", subID)
			ctx := context.Background()
			_, _ = fx.pool.Exec(ctx, `UPDATE public.subscriptions SET canceled_at = now(), price_cents = 2500 WHERE id = $1`, fx.subscriptionID)
			if tc.invoice != "" {
				fx.seedInvoice(t)
				_, _ = fx.pool.Exec(ctx, `UPDATE public.invoices SET status = $2 WHERE mollie_payment_id = $1`, fx.molliePaymentID, tc.invoice)
			}
			f, client := newPMMollie(t)
			svc := NewService(fx.pool, client, Config{WebhookBaseURL: "https://api.test"}, true)
			payment := &mollie.Payment{
				ID: fx.molliePaymentID, Status: "paid", SequenceType: mollie.SequenceTypeRecurring,
				SubscriptionID: subID, CustomerID: "cst_x",
				Amount:    mollie.Amount{Currency: "EUR", Value: "25.00"},
				CreatedAt: time.Now().Add(-48 * time.Hour), // created before the downgrade
			}
			if err := svc.recordRecurringPayment(ctx, payment); err != nil {
				t.Fatalf("recordRecurringPayment: %v", err)
			}
			if got := len(f.refunded) > 0; got != tc.wantRefund {
				t.Errorf("refunded = %v, want %v", got, tc.wantRefund)
			}
			var invoices int
			_ = fx.pool.QueryRow(ctx, `SELECT count(*) FROM public.invoices WHERE mollie_payment_id = $1 AND status = 'paid'`, payment.ID).Scan(&invoices)
			if tc.wantRefund && invoices != 0 && tc.invoice != "paid" {
				t.Errorf("refunded charge was also recorded as a paid invoice")
			}
		})
	}
}

// A failed or abandoned payment-method update changes nothing: it must
// not take the abandoned-checkout path (which cancels subscriptions).
func TestWebhook_FailedPaymentMethodUpdateIsIgnored(t *testing.T) {
	fx := setupPaymentFailureFixture(t)
	fx.seedSubscription(t, "active", "sub_pmf_"+fx.molliePaymentID)
	payID := "tr_pmf_" + fx.molliePaymentID
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": payID, "status": "canceled", "sequenceType": "first",
			"amount":   map[string]any{"currency": "EUR", "value": "0.00"},
			"metadata": map[string]any{"purpose": purposePaymentMethodUpdate, "subscription_id": fx.subscriptionID, "project_id": fx.projectID},
		})
	}))
	t.Cleanup(srv.Close)
	client := mollie.NewClient(mollie.Config{APIKey: "test_fake", Env: mollie.EnvTest, BaseURL: srv.URL})
	svc := NewService(fx.pool, client, Config{}, true)
	if err := svc.processPaymentWebhook(context.Background(), payID); err != nil {
		t.Fatalf("webhook: %v", err)
	}
	fx.assertSubscription(t, "active", false, false)
}
