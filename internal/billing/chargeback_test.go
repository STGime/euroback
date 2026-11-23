package billing

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/eurobase/euroback/internal/billing/mollie"
)

// chargebackFixture: an active subscription with a Mollie subscription
// and customer, and paid invoices (oldest first).
func chargebackFixture(t *testing.T, paidInvoices int) (*paymentFailureFixture, string, []string) {
	t.Helper()
	fx := setupPaymentFailureFixture(t)
	subID := "sub_cb_" + fx.molliePaymentID
	fx.seedSubscription(t, "active", subID)
	ctx := context.Background()
	if _, err := fx.pool.Exec(ctx,
		`UPDATE public.subscriptions SET mollie_customer_id = 'cst_cb', price_cents = 2500,
		        started_at = now() - interval '2 months', next_charge_at = now() + interval '20 days'
		  WHERE id = $1`, fx.subscriptionID); err != nil {
		t.Fatalf("prepare subscription: %v", err)
	}
	var payments []string
	for i := 0; i < paidInvoices; i++ {
		pid := "tr_cb" + string(rune('a'+i)) + "_" + fx.molliePaymentID
		if _, err := fx.pool.Exec(ctx,
			`INSERT INTO public.invoices (project_id, subscription_id, mollie_payment_id, amount_cents, currency, status, paid_at, billed_to_user_id)
			 VALUES ($1, $2, $3, 2500, 'EUR', 'paid', now() - make_interval(days => $4), $5)`,
			fx.projectID, fx.subscriptionID, pid, 30*(paidInvoices-1-i)+10, fx.ownerID); err != nil {
			t.Fatalf("seed invoice: %v", err)
		}
		payments = append(payments, pid)
	}
	return fx, subID, payments
}

func cbPayment(id, subID, chargedBack string) *mollie.Payment {
	p := &mollie.Payment{
		ID: id, Status: "paid", SequenceType: mollie.SequenceTypeRecurring,
		SubscriptionID: subID, CustomerID: "cst_cb",
		Amount: mollie.Amount{Currency: "EUR", Value: "25.00"}, CreatedAt: time.Now(),
	}
	if chargedBack != "" {
		p.AmountChargedBack = &mollie.Amount{Currency: "EUR", Value: chargedBack}
	}
	return p
}

func (fx *paymentFailureFixture) invoiceStatus(t *testing.T, paymentID string) string {
	t.Helper()
	var s string
	if err := fx.pool.QueryRow(context.Background(),
		`SELECT status FROM public.invoices WHERE mollie_payment_id = $1`, paymentID).Scan(&s); err != nil {
		t.Fatalf("read invoice: %v", err)
	}
	return s
}

func (fx *paymentFailureFixture) subscriptionState(t *testing.T) (status string, pastDue, canceled bool) {
	t.Helper()
	if err := fx.pool.QueryRow(context.Background(),
		`SELECT status, past_due_since IS NOT NULL, canceled_at IS NOT NULL FROM public.subscriptions WHERE id = $1`,
		fx.subscriptionID).Scan(&status, &pastDue, &canceled); err != nil {
		t.Fatalf("read subscription: %v", err)
	}
	return
}

// A chargeback of the payment for the current period stops the
// subscription at Mollie, puts ours past due (Pay now stays possible:
// not cancelled), mails the owner once and alerts ops.
func TestChargeback_CurrentPeriod(t *testing.T) {
	fx, subID, payments := chargebackFixture(t, 2)
	f, client := newPMMollie(t)
	mailer := &fakeMailer{}
	var alerts []string
	svc := NewService(fx.pool, client, Config{}, true)
	svc.invoiceMailer = mailer
	svc.opsAlert = func(title, desc string) { alerts = append(alerts, title+": "+desc) }
	ctx := context.Background()

	latest := payments[1]
	handled, err := svc.processChargeback(ctx, cbPayment(latest, subID, "25.00"))
	if err != nil || !handled {
		t.Fatalf("processChargeback: handled=%v err=%v", handled, err)
	}
	if got := fx.invoiceStatus(t, latest); got != "charged_back" {
		t.Errorf("invoice status = %q, want charged_back", got)
	}
	if got := fx.invoiceStatus(t, payments[0]); got != "paid" {
		t.Errorf("earlier invoice status = %q, want paid", got)
	}
	if status, pastDue, canceled := fx.subscriptionState(t); status != "past_due" || !pastDue || canceled {
		t.Errorf("subscription = %s past_due_since=%v canceled_at=%v, want past_due, set, unset", status, pastDue, canceled)
	}
	if len(f.canceled) != 1 || f.canceled[0] != subID {
		t.Errorf("Mollie cancels = %v, want [%s]", f.canceled, subID)
	}
	if mailer.calls != 1 || !strings.Contains(mailer.lastSubject, "reversed") {
		t.Errorf("mails = %d (subject %q), want 1 about the reversed payment", mailer.calls, mailer.lastSubject)
	}
	if len(alerts) != 1 || !strings.Contains(alerts[0], latest) {
		t.Errorf("alerts = %v", alerts)
	}

	// Redelivery changes nothing.
	if handled, err := svc.processChargeback(ctx, cbPayment(latest, subID, "25.00")); err != nil || !handled {
		t.Fatalf("redelivery: handled=%v err=%v", handled, err)
	}
	if mailer.calls != 1 || len(f.canceled) != 1 || len(alerts) != 1 {
		t.Errorf("redelivery: mails %d, cancels %d, alerts %d; want 1, 1, 1", mailer.calls, len(f.canceled), len(alerts))
	}

	// Mollie reports it no longer charged back: invoice paid again,
	// subscription left as it is.
	if handled, err := svc.processChargeback(ctx, cbPayment(latest, subID, "")); err != nil || !handled {
		t.Fatalf("reversal: handled=%v err=%v", handled, err)
	}
	if got := fx.invoiceStatus(t, latest); got != "paid" {
		t.Errorf("after reversal invoice = %q, want paid", got)
	}
	if status, _, _ := fx.subscriptionState(t); status != "past_due" {
		t.Errorf("after reversal subscription = %q, want still past_due", status)
	}
	if len(alerts) != 2 {
		t.Errorf("alerts after reversal = %d, want 2", len(alerts))
	}

	// A plain paid payment is not ours to handle.
	if handled, err := svc.processChargeback(ctx, cbPayment(payments[0], subID, "")); err != nil || handled {
		t.Errorf("ordinary payment: handled=%v err=%v, want false, nil", handled, err)
	}
}

// A chargeback of an earlier period only marks that invoice.
func TestChargeback_EarlierPeriod(t *testing.T) {
	fx, subID, payments := chargebackFixture(t, 2)
	f, client := newPMMollie(t)
	mailer := &fakeMailer{}
	svc := NewService(fx.pool, client, Config{}, true)
	svc.invoiceMailer = mailer

	if handled, err := svc.processChargeback(context.Background(), cbPayment(payments[0], subID, "25.00")); err != nil || !handled {
		t.Fatalf("processChargeback: handled=%v err=%v", handled, err)
	}
	if got := fx.invoiceStatus(t, payments[0]); got != "charged_back" {
		t.Errorf("invoice status = %q, want charged_back", got)
	}
	if status, _, _ := fx.subscriptionState(t); status != "active" {
		t.Errorf("subscription = %q, want active", status)
	}
	if len(f.canceled) != 0 || mailer.calls != 0 {
		t.Errorf("cancels %v, mails %d; want none", f.canceled, mailer.calls)
	}
}

// A partial chargeback is left to ops.
func TestChargeback_PartialLeftToOps(t *testing.T) {
	fx, subID, payments := chargebackFixture(t, 1)
	f, client := newPMMollie(t)
	var alerts int
	svc := NewService(fx.pool, client, Config{}, true)
	svc.opsAlert = func(string, string) { alerts++ }

	if handled, err := svc.processChargeback(context.Background(), cbPayment(payments[0], subID, "10.00")); err != nil || !handled {
		t.Fatalf("processChargeback: handled=%v err=%v", handled, err)
	}
	if got := fx.invoiceStatus(t, payments[0]); got != "paid" {
		t.Errorf("invoice status = %q, want paid", got)
	}
	if status, _, _ := fx.subscriptionState(t); status != "active" || len(f.canceled) != 0 || alerts != 1 {
		t.Errorf("subscription %q, cancels %v, alerts %d; want active, none, 1", status, f.canceled, alerts)
	}
}
