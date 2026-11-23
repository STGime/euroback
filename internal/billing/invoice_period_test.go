package billing

import (
	"context"
	"testing"
	"time"

	"github.com/eurobase/euroback/internal/billing/mollie"
)

func TestInvoicePeriodFor(t *testing.T) {
	d := func(s string) *time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return &v
	}
	created := *d("2026-11-13T02:37:22Z")
	cases := []struct {
		name               string
		start, end, paidAt *time.Time
		wantFrom, wantTo   string
	}{
		{"recorded period", d("2026-11-13T00:00:00Z"), d("2026-12-13T00:00:00Z"), d("2026-11-13T02:37:22Z"), "2026-11-13", "2026-12-13"},
		{"older invoice: payment date plus one month", nil, nil, d("2026-11-13T02:37:22Z"), "2026-11-13", "2026-12-13"},
		{"unpaid older invoice: issue date", nil, nil, nil, "2026-11-13", "2026-12-13"},
		{"start without end", d("2026-01-31T10:00:00Z"), nil, nil, "2026-01-31", "2026-02-28"},
		{"end not after start", d("2026-11-13T00:00:00Z"), d("2026-11-13T00:00:00Z"), nil, "2026-11-13", "2026-12-13"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			from, to := invoicePeriodFor(tc.start, tc.end, tc.paidAt, created)
			if got := from.Format("2006-01-02"); got != tc.wantFrom {
				t.Errorf("from = %s, want %s", got, tc.wantFrom)
			}
			if got := to.Format("2006-01-02"); got != tc.wantTo {
				t.Errorf("to = %s, want %s", got, tc.wantTo)
			}
		})
	}
}

func TestChargePeriod(t *testing.T) {
	due := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	next := time.Date(2026, 12, 13, 0, 0, 0, 0, time.UTC)
	if s, e := chargePeriod(&due, &next); s == nil || e == nil || !s.Equal(due) || !e.Equal(next) {
		t.Errorf("chargePeriod(due, next) = %v, %v", s, e)
	}
	if s, e := chargePeriod(nil, &next); s != nil || e != nil {
		t.Error("no due date must give no period")
	}
	if s, e := chargePeriod(&next, &due); s != nil || e != nil {
		t.Error("next before due must give no period")
	}
}

// A renewal's invoice covers the month it pays for, not the subscription
// since its start (the PDF used to print "start – next charge").
func TestRecordRecurringPayment_InvoicePeriod(t *testing.T) {
	fx := setupPaymentFailureFixture(t)
	subID := "sub_period_" + fx.molliePaymentID
	fx.seedSubscription(t, "active", subID)
	ctx := context.Background()
	due := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -1)
	if _, err := fx.pool.Exec(ctx,
		`UPDATE public.subscriptions
		    SET price_cents = 2500, mollie_customer_id = 'cst_period',
		        started_at = $2::timestamptz - interval '2 months 1 day', next_charge_at = $2
		  WHERE id = $1`,
		fx.subscriptionID, due,
	); err != nil {
		t.Fatalf("prepare subscription: %v", err)
	}
	_, client := newPMMollie(t) // no nextPaymentDate: next charge = due + 1 month
	svc := NewService(fx.pool, client, Config{WebhookBaseURL: "https://api.test"}, true)
	payment := &mollie.Payment{
		ID: fx.molliePaymentID, Status: "paid", SequenceType: mollie.SequenceTypeRecurring,
		SubscriptionID: subID, CustomerID: "cst_period",
		Amount: mollie.Amount{Currency: "EUR", Value: "25.00"}, CreatedAt: time.Now(),
	}
	if err := svc.recordRecurringPayment(ctx, payment); err != nil {
		t.Fatalf("recordRecurringPayment: %v", err)
	}

	var invoiceID string
	var start, end *time.Time
	if err := fx.pool.QueryRow(ctx,
		`SELECT id, period_start, period_end FROM public.invoices WHERE mollie_payment_id = $1`, payment.ID,
	).Scan(&invoiceID, &start, &end); err != nil {
		t.Fatalf("read invoice: %v", err)
	}
	wantEnd := addMonthClamped(due)
	if start == nil || end == nil || !start.Equal(due) || !end.Equal(wantEnd) {
		t.Fatalf("invoice period = %v – %v, want %v – %v", start, end, due, wantEnd)
	}

	data, _, err := svc.loadInvoiceData(ctx, invoiceID)
	if err != nil {
		t.Fatalf("loadInvoiceData: %v", err)
	}
	if !data.PeriodFrom.Equal(due) || !data.PeriodTo.Equal(wantEnd) {
		t.Errorf("PDF period = %v – %v, want %v – %v", data.PeriodFrom, data.PeriodTo, due, wantEnd)
	}
}
