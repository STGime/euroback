package billing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/eurobase/euroback/internal/billing/mollie"
)

// seedPayNow makes the fixture a past-due Pro project paid by its owner,
// with a billing profile and an invoice-less overdue subscription.
func seedPayNow(t *testing.T, fx *paymentFailureFixture) (cust, oldMollieSub string) {
	t.Helper()
	ctx := context.Background()
	cust = "cst_pn_" + fx.molliePaymentID
	oldMollieSub = "sub_pn_" + fx.molliePaymentID
	fx.seedSubscription(t, "past_due", oldMollieSub)
	seedPayer(t, fx, cust)
	if _, err := fx.pool.Exec(ctx,
		`UPDATE public.subscriptions SET price_cents = 2500, past_due_since = now() - interval '2 days' WHERE id = $1`,
		fx.subscriptionID); err != nil {
		t.Fatalf("seed price: %v", err)
	}
	if _, err := fx.pool.Exec(ctx, `UPDATE public.projects SET plan = 'pro' WHERE id = $1`, fx.projectID); err != nil {
		t.Fatalf("seed plan: %v", err)
	}
	if _, err := fx.pool.Exec(ctx,
		`INSERT INTO public.billing_profiles
		    (platform_user_id, entity_type, legal_name, street_address, postal_code, city, country)
		 VALUES ($1, 'individual', 'Pay Now', 'Street 1', '10115', 'Berlin', 'DE')`,
		fx.ownerID); err != nil {
		t.Fatalf("seed billing profile: %v", err)
	}
	t.Cleanup(func() {
		_, _ = fx.pool.Exec(context.Background(), `DELETE FROM public.billing_profiles WHERE platform_user_id = $1`, fx.ownerID)
	})
	return cust, oldMollieSub
}

func TestStartPayNow(t *testing.T) {
	fx := setupPaymentFailureFixture(t)
	cust, _ := seedPayNow(t, fx)
	f, client := newPMMollie(t)
	svc := NewService(fx.pool, client, Config{ConsoleBaseURL: "https://console.test", WebhookBaseURL: "https://api.test"}, true)
	ctx := context.Background()

	url, err := svc.StartPayNow(ctx, fx.ownerID, fx.projectID)
	if err != nil || url != "https://mollie.test/pm" {
		t.Fatalf("start: url %q err %v", url, err)
	}
	p := f.created[0]
	amount, _ := p["amount"].(map[string]any)
	meta, _ := p["metadata"].(map[string]any)
	if amount["value"] != "25.00" || p["sequenceType"] != "first" || p["customerId"] != cust ||
		meta["purpose"] != purposePayNow || meta["subscription_id"] != fx.subscriptionID {
		t.Errorf("payment body = %v", p)
	}
	if _, restricted := p["method"]; restricted {
		t.Errorf("pay now restricts methods: %v", p["method"])
	}
	var invStatus, invSub string
	if err := fx.pool.QueryRow(ctx,
		`SELECT status, subscription_id FROM public.invoices WHERE mollie_payment_id = 'tr_pm_new'`,
	).Scan(&invStatus, &invSub); err != nil || invStatus != "pending" || invSub != fx.subscriptionID {
		t.Errorf("invoice: status %q sub %q err %v", invStatus, invSub, err)
	}
	_, _ = fx.pool.Exec(ctx, `DELETE FROM public.invoices WHERE mollie_payment_id = 'tr_pm_new'`)

	// Not overdue.
	_, _ = fx.pool.Exec(ctx, `UPDATE public.subscriptions SET status = 'active', past_due_since = NULL WHERE id = $1`, fx.subscriptionID)
	if _, err := svc.StartPayNow(ctx, fx.ownerID, fx.projectID); !errors.Is(err, ErrNotOverdue) {
		t.Errorf("active: err = %v", err)
	}
	// Overdue but cancelled by the owner.
	_, _ = fx.pool.Exec(ctx, `UPDATE public.subscriptions SET status = 'past_due', canceled_at = now() WHERE id = $1`, fx.subscriptionID)
	if _, err := svc.StartPayNow(ctx, fx.ownerID, fx.projectID); !errors.Is(err, ErrNotOverdue) {
		t.Errorf("cancelled: err = %v", err)
	}
	// Someone else.
	if _, err := svc.StartPayNow(ctx, "00000000-0000-0000-0000-000000000000", fx.projectID); !errors.Is(err, ErrSubscriptionNotFound) {
		t.Errorf("other user: err = %v", err)
	}
}

func TestApplyPayNow(t *testing.T) {
	cases := []struct {
		name        string
		prepare     func(t *testing.T, fx *paymentFailureFixture)
		wantSettled bool // replacement active, project on Pro
		wantCancel  bool // old Mollie subscription cancelled
		wantRefund  bool
	}{
		{"settles a past-due subscription", nil, true, true, false},
		{"downgraded meanwhile: brings Pro back", func(t *testing.T, fx *paymentFailureFixture) {
			_, _ = fx.pool.Exec(context.Background(), `UPDATE public.subscriptions SET status = 'expired', canceled_at = now() WHERE id = $1`, fx.subscriptionID)
			_, _ = fx.pool.Exec(context.Background(), `UPDATE public.projects SET plan = 'free' WHERE id = $1`, fx.projectID)
		}, true, false, false},
		{"retry went through meanwhile: refunded", func(t *testing.T, fx *paymentFailureFixture) {
			_, _ = fx.pool.Exec(context.Background(), `UPDATE public.subscriptions SET status = 'active', past_due_since = NULL WHERE id = $1`, fx.subscriptionID)
		}, false, false, true},
		{"owner cancelled meanwhile: refunded", func(t *testing.T, fx *paymentFailureFixture) {
			_, _ = fx.pool.Exec(context.Background(), `UPDATE public.subscriptions SET status = 'canceled', canceled_at = now() WHERE id = $1`, fx.subscriptionID)
		}, false, false, true},
		{"payer no longer the owner: refunded", func(t *testing.T, fx *paymentFailureFixture) {
			_, _ = fx.pool.Exec(context.Background(), `UPDATE public.platform_users SET mollie_customer_id = 'cst_other' WHERE id = $1`, fx.ownerID)
		}, false, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := setupPaymentFailureFixture(t)
			cust, oldSub := seedPayNow(t, fx)
			if tc.prepare != nil {
				tc.prepare(t, fx)
			}
			f, client := newPMMollie(t)
			f.addMandate("mdt_new", "creditcard", time.Now())
			svc := NewService(fx.pool, client, Config{WebhookBaseURL: "https://api.test"}, true)
			ctx := context.Background()
			payID := "tr_pn_" + fx.molliePaymentID
			if _, err := fx.pool.Exec(ctx,
				`INSERT INTO public.invoices (project_id, subscription_id, mollie_payment_id, amount_cents, currency, status)
				 VALUES ($1, $2, $3, 2500, 'EUR', 'pending')`,
				fx.projectID, fx.subscriptionID, payID); err != nil {
				t.Fatalf("seed invoice: %v", err)
			}
			payment := &mollie.Payment{
				ID: payID, Status: "paid", SequenceType: mollie.SequenceTypeFirst,
				CustomerID: cust, MandateID: "mdt_new", Amount: mollie.Amount{Currency: "EUR", Value: "25.00"},
				Description: "Eurobase pro — test",
				Metadata:    map[string]string{"purpose": purposePayNow, "subscription_id": fx.subscriptionID, "project_id": fx.projectID, "plan_code": "pro"},
			}
			if err := svc.processPayNowWebhook(ctx, payment); err != nil {
				t.Fatalf("apply: %v", err)
			}

			if got := len(f.canceled) > 0; got != tc.wantCancel {
				t.Errorf("old subscription cancelled = %v (%v), want %v", got, f.canceled, tc.wantCancel)
			}
			if tc.wantCancel && f.canceled[0] != oldSub {
				t.Errorf("cancelled %q, want %q", f.canceled[0], oldSub)
			}
			if got := len(f.refunded) > 0; got != tc.wantRefund {
				t.Errorf("refunded = %v, want %v", got, tc.wantRefund)
			}

			var newStatus, newMollie, plan, invStatus string
			var invOnNew bool
			err := fx.pool.QueryRow(ctx,
				`SELECT s.status, s.mollie_subscription_id FROM public.subscriptions s WHERE s.mollie_payment_id = $1`, payID,
			).Scan(&newStatus, &newMollie)
			_ = fx.pool.QueryRow(ctx, `SELECT plan FROM public.projects WHERE id = $1`, fx.projectID).Scan(&plan)
			_ = fx.pool.QueryRow(ctx,
				`SELECT i.status, i.subscription_id IS DISTINCT FROM $2::uuid FROM public.invoices i WHERE i.mollie_payment_id = $1`,
				payID, fx.subscriptionID).Scan(&invStatus, &invOnNew)

			if tc.wantSettled {
				if err != nil || newStatus != "active" || newMollie == "" {
					t.Fatalf("replacement: status %q mollie %q err %v", newStatus, newMollie, err)
				}
				if len(f.newSubs) != 1 || f.newSubs[0]["mandateId"] != "mdt_new" {
					t.Errorf("created at Mollie: %v", f.newSubs)
				}
				var oldStatus string
				_ = fx.pool.QueryRow(ctx, `SELECT status FROM public.subscriptions WHERE id = $1`, fx.subscriptionID).Scan(&oldStatus)
				if oldStatus != "expired" {
					t.Errorf("old subscription status = %q, want expired", oldStatus)
				}
				if plan != "pro" || invStatus != "paid" || !invOnNew {
					t.Errorf("plan %q invoice %q on new subscription %v", plan, invStatus, invOnNew)
				}
				// Redelivery creates nothing new.
				if err := svc.processPayNowWebhook(ctx, payment); err != nil {
					t.Fatalf("redelivery: %v", err)
				}
				if len(f.newSubs) != 1 || len(f.canceled) > 1 {
					t.Errorf("redelivery acted again: created %d, cancelled %v", len(f.newSubs), f.canceled)
				}
			} else {
				if err == nil {
					t.Errorf("replacement subscription created (status %q)", newStatus)
				}
				if len(f.newSubs) != 0 {
					t.Errorf("created at Mollie: %v", f.newSubs)
				}
				if invStatus != "refunded" {
					t.Errorf("invoice status = %q, want refunded", invStatus)
				}
			}
		})
	}
}

// Another live subscription started while the owner was paying (e.g. a
// fresh checkout after the downgrade): the replacement is undone at
// Mollie and the payment refunded.
func TestApplyPayNow_AnotherSubscriptionStarted(t *testing.T) {
	fx := setupPaymentFailureFixture(t)
	cust, _ := seedPayNow(t, fx)
	ctx := context.Background()
	_, _ = fx.pool.Exec(ctx, `UPDATE public.subscriptions SET status = 'expired', canceled_at = now() WHERE id = $1`, fx.subscriptionID)
	if _, err := fx.pool.Exec(ctx,
		`INSERT INTO public.subscriptions (project_id, plan, status, price_cents) VALUES ($1, 'pro', 'incomplete', 2500)`,
		fx.projectID); err != nil {
		t.Fatalf("seed other subscription: %v", err)
	}
	f, client := newPMMollie(t)
	svc := NewService(fx.pool, client, Config{WebhookBaseURL: "https://api.test"}, true)
	payment := &mollie.Payment{
		ID: "tr_pn2_" + fx.molliePaymentID, Status: "paid", SequenceType: mollie.SequenceTypeFirst,
		CustomerID: cust, MandateID: "mdt_new", Amount: mollie.Amount{Currency: "EUR", Value: "25.00"},
		Metadata: map[string]string{"purpose": purposePayNow, "subscription_id": fx.subscriptionID, "project_id": fx.projectID, "plan_code": "pro"},
	}
	if err := svc.processPayNowWebhook(ctx, payment); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(f.newSubs) != 1 || len(f.canceled) != 1 || f.canceled[0] != "sub_new_1" || len(f.refunded) != 1 {
		t.Errorf("created %v, cancelled %v, refunded %v", f.newSubs, f.canceled, f.refunded)
	}
	var n int
	_ = fx.pool.QueryRow(ctx, `SELECT count(*) FROM public.subscriptions WHERE mollie_payment_id = $1`, payment.ID).Scan(&n)
	if n != 0 {
		t.Errorf("replacement row kept")
	}
}

// An abandoned or declined Pay now payment leaves the subscription past
// due and marks only its own invoice failed.
func TestPayNow_FailedPaymentChangesNothing(t *testing.T) {
	fx := setupPaymentFailureFixture(t)
	cust, _ := seedPayNow(t, fx)
	ctx := context.Background()
	payID := "tr_pn3_" + fx.molliePaymentID
	_, _ = fx.pool.Exec(ctx,
		`INSERT INTO public.invoices (project_id, subscription_id, mollie_payment_id, amount_cents, currency, status)
		 VALUES ($1, $2, $3, 2500, 'EUR', 'pending')`, fx.projectID, fx.subscriptionID, payID)
	_, client := newPMMollie(t)
	svc := NewService(fx.pool, client, Config{}, true)
	payment := &mollie.Payment{ID: payID, Status: "canceled", SequenceType: mollie.SequenceTypeFirst, CustomerID: cust,
		Metadata: map[string]string{"purpose": purposePayNow, "subscription_id": fx.subscriptionID, "project_id": fx.projectID}}
	if err := svc.processPayNowWebhook(ctx, payment); err != nil {
		t.Fatalf("webhook: %v", err)
	}
	var status, inv string
	_ = fx.pool.QueryRow(ctx, `SELECT status FROM public.subscriptions WHERE id = $1`, fx.subscriptionID).Scan(&status)
	_ = fx.pool.QueryRow(ctx, `SELECT status FROM public.invoices WHERE mollie_payment_id = $1`, payID).Scan(&inv)
	if status != "past_due" || inv != "failed" {
		t.Errorf("subscription %q invoice %q", status, inv)
	}
}
