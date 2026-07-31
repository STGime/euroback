package billing

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeInvoiceMailer captures every SendRaw call so tests can
// assert both the buyer send + the accounting BCC. Optionally
// scripts errors to exercise the "buyer bounce, accounting still
// sends" path.
type fakeInvoiceMailer struct {
	sends   []invoiceMailerSend
	err     error // if non-nil, every send returns it
	errFor  string // if non-empty, only sends to this recipient error
}

type invoiceMailerSend struct {
	to      string
	subject string
	body    string
}

func (f *fakeInvoiceMailer) SendRaw(ctx context.Context, to, subject, body string) error {
	f.sends = append(f.sends, invoiceMailerSend{to: to, subject: subject, body: body})
	if f.err != nil {
		return f.err
	}
	if f.errFor != "" && to == f.errFor {
		return errors.New("scripted failure")
	}
	return nil
}

// TestSendInvoiceReadyMail_BothRecipients verifies buyer + BCC
// each get a send, with the "[copy]" prefix on the accounting
// one so operator inbox rules can filter it.
func TestSendInvoiceReadyMail_BothRecipients(t *testing.T) {
	m := &fakeInvoiceMailer{}
	svc := &Service{invoiceMailer: m}
	svc.sendInvoiceReadyMail(context.Background(), invoiceEmailState{
		InvoiceNumber:     "EB-2026-000001",
		ProjectName:       "MyProject",
		AmountCents:       1900,
		Currency:          "EUR",
		BuyerEmail:        "alice@example.com",
		BuyerName:         "Alice",
		ConsoleBillingURL: "https://console.eurobase.app/billing",
	})

	if len(m.sends) != 2 {
		t.Fatalf("want 2 sends (buyer + BCC), got %d", len(m.sends))
	}
	buyer, bcc := m.sends[0], m.sends[1]
	if buyer.to != "alice@example.com" {
		t.Errorf("buyer recipient = %q", buyer.to)
	}
	if bcc.to != accountingBCC {
		t.Errorf("BCC recipient = %q, want %q", bcc.to, accountingBCC)
	}
	if !strings.HasPrefix(bcc.subject, "[copy]") {
		t.Errorf("BCC subject missing [copy] prefix: %q", bcc.subject)
	}
	if !strings.Contains(buyer.body, "EB-2026-000001") {
		t.Error("buyer body missing invoice number")
	}
	if !strings.Contains(buyer.body, "MyProject") {
		t.Error("buyer body missing project name")
	}
	if !strings.Contains(buyer.body, "€19.00") {
		t.Error("buyer body missing formatted amount")
	}
	if !strings.Contains(buyer.body, "Ahtri 12") {
		t.Error("buyer body missing Estonian entity footer")
	}
	// VAT statement uses the HTML entity &sect;19 in the
	// rendered body (browser-decoded to §). Assert on the
	// semantic marker "Not VAT-registered" rather than the
	// raw §, so a future template refactor that swaps entity
	// encoding doesn't break the test.
	if !strings.Contains(buyer.body, "Not VAT-registered") {
		t.Error("buyer body missing VAT non-registration statement")
	}
	if !strings.Contains(buyer.body, "https://console.eurobase.app/billing") {
		t.Error("buyer body missing console deep link")
	}
}

// TestSendInvoiceReadyMail_BuyerBounceStillSendsBCC verifies
// the accounting copy fires even when the buyer send fails —
// otherwise a bad buyer address would silently drop our own
// audit trail.
func TestSendInvoiceReadyMail_BuyerBounceStillSendsBCC(t *testing.T) {
	m := &fakeInvoiceMailer{errFor: "bad@example.com"}
	svc := &Service{invoiceMailer: m}
	svc.sendInvoiceReadyMail(context.Background(), invoiceEmailState{
		InvoiceNumber:     "EB-2026-000002",
		BuyerEmail:        "bad@example.com",
		AmountCents:       1900,
		Currency:          "EUR",
		ProjectName:       "X",
		ConsoleBillingURL: "https://console.eurobase.app/billing",
	})
	if len(m.sends) != 2 {
		t.Fatalf("want 2 sends even on buyer bounce, got %d", len(m.sends))
	}
	if m.sends[1].to != accountingBCC {
		t.Errorf("second send should be accounting BCC, got %q", m.sends[1].to)
	}
}

// TestSendInvoiceReadyMail_NilMailerIsNoop covers dev
// environments that boot without TEM creds. The service must
// not panic; the invoice PDF is still in S3 and reachable via
// the download endpoint.
func TestSendInvoiceReadyMail_NilMailerIsNoop(t *testing.T) {
	svc := &Service{invoiceMailer: nil}
	svc.sendInvoiceReadyMail(context.Background(), invoiceEmailState{
		BuyerEmail: "x@example.com",
	})
}

// TestSendInvoiceReadyMail_EmptyBuyerSkipsBoth. If we can't
// reach the buyer we also skip the BCC — the accounting copy
// exists to mirror what we sent to the customer, not to spam
// billing@ with un-deliverable invoices.
func TestSendInvoiceReadyMail_EmptyBuyerSkipsBoth(t *testing.T) {
	m := &fakeInvoiceMailer{}
	svc := &Service{invoiceMailer: m}
	svc.sendInvoiceReadyMail(context.Background(), invoiceEmailState{
		BuyerEmail: "",
	})
	if len(m.sends) != 0 {
		t.Errorf("want 0 sends on empty buyer, got %d", len(m.sends))
	}
}

// TestSendInvoiceReadyMail_NoNameGreeting handles the case
// where display_name is empty (Google OAuth signup often
// yields this) — greeting should degrade to "Hi," rather
// than "Hi , " (double punctuation).
func TestSendInvoiceReadyMail_NoNameGreeting(t *testing.T) {
	m := &fakeInvoiceMailer{}
	svc := &Service{invoiceMailer: m}
	svc.sendInvoiceReadyMail(context.Background(), invoiceEmailState{
		InvoiceNumber:     "EB-2026-000003",
		BuyerEmail:        "x@example.com",
		BuyerName:         "",
		AmountCents:       1900,
		Currency:          "EUR",
		ProjectName:       "X",
		ConsoleBillingURL: "https://console.eurobase.app/billing",
	})
	body := m.sends[0].body
	if strings.Contains(body, "Hi ,") || strings.Contains(body, "Hi  ") {
		t.Errorf("body has malformed greeting: %s", body[:200])
	}
	if !strings.Contains(body, "Hi,") {
		t.Error("body missing bare 'Hi,' greeting when name empty")
	}
}
