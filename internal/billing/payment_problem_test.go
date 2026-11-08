package billing

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/eurobase/euroback/internal/billing/mollie"
)

func TestRenderPaymentProblemMail(t *testing.T) {
	at := time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)

	subject, body := renderPaymentProblemMail("pid-1", "Shop <b>", at, paymentProblemChargeFailed)
	if !strings.Contains(subject, "payment for Shop <b> failed") {
		t.Errorf("subject = %q", subject)
	}
	for _, want := range []string{"Shop &lt;b&gt;", "15 November 2026", "retries the charge", "/p/pid-1/billing", "Nothing is deleted"} {
		if !strings.Contains(body, want) {
			t.Errorf("charge-failed body lacks %q", want)
		}
	}
	if strings.Contains(body, "Shop <b>") {
		t.Error("project name not escaped in body")
	}

	subject, body = renderPaymentProblemMail("pid-1", "Shop", at, paymentProblemStoppedAtMollie)
	if !strings.Contains(subject, "has stopped") {
		t.Errorf("subject = %q", subject)
	}
	if !strings.Contains(body, "No further charges will be attempted") || strings.Contains(body, "retries the charge") {
		t.Error("stopped body has the wrong explanation")
	}
}

func TestMollieSubscriptionStopped(t *testing.T) {
	for status, want := range map[string]bool{
		"active": false, "pending": false, "canceled": true, "suspended": true, "completed": true,
	} {
		if got := mollieSubscriptionStopped(status); got != want {
			t.Errorf("%s: got %v, want %v", status, got, want)
		}
	}
}

// The first failed recurring charge mails the owner once; Mollie's
// retries (subscription already past_due) don't mail again.
func TestMarkPaymentFailure_MailsOwnerOnce(t *testing.T) {
	fx := setupPaymentFailureFixture(t)
	fx.seedInvoice(t)
	fx.seedSubscription(t, "active", "sub_test_mail_"+fx.molliePaymentID)

	mailer := &fakeMailer{}
	svc := &Service{pool: fx.pool, invoiceMailer: mailer}
	payment := &mollie.Payment{ID: fx.molliePaymentID, Status: "failed", SubscriptionID: fx.mollieSubscriptionID}

	if err := svc.markPaymentFailure(context.Background(), payment); err != nil {
		t.Fatalf("markPaymentFailure: %v", err)
	}
	if mailer.calls != 1 {
		t.Fatalf("mails after first failure = %d, want 1", mailer.calls)
	}
	if !strings.HasPrefix(mailer.lastTo, "billwh-") || !strings.Contains(mailer.lastSubject, "failed") {
		t.Errorf("mail to %q subject %q", mailer.lastTo, mailer.lastSubject)
	}
	if !strings.Contains(mailer.lastBody, time.Now().Add(pastDueGracePeriod).UTC().Format("2 January 2006")) {
		t.Error("mail doesn't name the downgrade date")
	}

	// Retry fails again.
	if err := svc.markPaymentFailure(context.Background(), payment); err != nil {
		t.Fatalf("markPaymentFailure (retry): %v", err)
	}
	if mailer.calls != 1 {
		t.Errorf("mails after retry = %d, want still 1", mailer.calls)
	}
}

// stoppedMollie answers GET /customers/{c}/subscriptions/{s} with the
// status registered for s, "active" for anything else (so subscriptions
// other tests left in a shared database stay untouched).
func stoppedMollie(t *testing.T, statuses map[string]string) *mollie.Client {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if r.Method != http.MethodGet || len(parts) != 4 || parts[0] != "customers" || parts[2] != "subscriptions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		status, ok := statuses[parts[3]]
		if !ok {
			status = "active"
		}
		if status == "gone" {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"status": 404})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": parts[3], "customerId": parts[1], "status": status})
	}))
	t.Cleanup(srv.Close)
	return mollie.NewClient(mollie.Config{APIKey: "test_fake", Env: mollie.EnvTest, BaseURL: srv.URL})
}

func TestReconcileStoppedSubscriptions(t *testing.T) {
	cases := []struct {
		name         string
		mollieStatus string
		canceledAt   bool          // owner cancelled at period end
		overdue      time.Duration // how long next_charge_at is past
		wantStatus   string
		wantMail     bool
	}{
		{"canceled at Mollie", "canceled", false, 3 * 24 * time.Hour, "past_due", true},
		{"suspended at Mollie", "suspended", false, 3 * 24 * time.Hour, "past_due", true},
		{"gone at Mollie", "gone", false, 3 * 24 * time.Hour, "past_due", true},
		{"still active (SEPA pending)", "active", false, 3 * 24 * time.Hour, "active", false},
		{"not overdue long enough", "canceled", false, 12 * time.Hour, "active", false},
		{"owner cancelled at period end", "canceled", true, 3 * 24 * time.Hour, "active", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := setupPaymentFailureFixture(t)
			subID := "sub_test_rec_" + fx.molliePaymentID
			fx.seedSubscription(t, "active", subID)
			if _, err := fx.pool.Exec(context.Background(),
				`UPDATE public.subscriptions
				    SET mollie_customer_id = 'cst_test_rec',
				        next_charge_at = now() - $2::interval,
				        canceled_at = CASE WHEN $3::boolean THEN now() - interval '20 days' END
				  WHERE id = $1`,
				fx.subscriptionID, fmt.Sprintf("%d seconds", int(tc.overdue.Seconds())), tc.canceledAt,
			); err != nil {
				t.Fatalf("prepare subscription: %v", err)
			}

			mailer := &fakeMailer{}
			ds := NewDowngradeService(fx.pool, stoppedMollie(t, map[string]string{subID: tc.mollieStatus}), mailer, "")
			ds.reconcileStoppedSubscriptions(context.Background())

			var status string
			var pastDueSince *time.Time
			if err := fx.pool.QueryRow(context.Background(),
				`SELECT status, past_due_since FROM public.subscriptions WHERE id = $1`, fx.subscriptionID,
			).Scan(&status, &pastDueSince); err != nil {
				t.Fatalf("read subscription: %v", err)
			}
			if status != tc.wantStatus {
				t.Errorf("status = %q, want %q", status, tc.wantStatus)
			}
			if (tc.wantStatus == "past_due") != (pastDueSince != nil) {
				t.Errorf("past_due_since = %v for status %q", pastDueSince, status)
			}
			if gotMail := mailer.calls > 0; gotMail != tc.wantMail {
				t.Errorf("mail sent = %v, want %v", gotMail, tc.wantMail)
			}
			if tc.wantMail && !strings.Contains(mailer.lastSubject, "has stopped") {
				t.Errorf("subject = %q", mailer.lastSubject)
			}

			// A second pass changes nothing and sends no second mail.
			calls := mailer.calls
			ds.reconcileStoppedSubscriptions(context.Background())
			if mailer.calls != calls {
				t.Errorf("second pass sent %d more mails", mailer.calls-calls)
			}
		})
	}
}
