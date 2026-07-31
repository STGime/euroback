package billing

import (
	"context"
	"fmt"
	"log/slog"
)

// accountingBCC is the internal address we CC on every outbound
// invoice mail. Kept as a package constant rather than an env
// var — there's exactly one canonical accounting inbox for the
// entity, and letting an operator override it via env is a
// footgun (a mistyped value would silently drop copies from our
// own accounting trail without any obvious warning).
const accountingBCC = "billing@eurobase.app"

// invoiceMailer is the surface the auto-email path uses. Kept
// as an interface for the same reasons as downgradeMailer:
// tests inject a fake, nil is a no-op fallback for dev
// environments without TEM credentials.
type invoiceMailer interface {
	SendRaw(ctx context.Context, to, subject, htmlBody string) error
}

// invoiceEmailState is the tiny subset of InvoiceData the mail
// template needs. Kept separate from InvoiceData so a future
// change to the PDF layout doesn't accidentally break the mail.
type invoiceEmailState struct {
	InvoiceNumber string
	ProjectName   string
	AmountCents   int
	Currency      string
	BuyerEmail    string
	BuyerName     string
	ConsoleBillingURL string // deep link into console → project billing page
}

// WithInvoiceMailer wires the platform email service. Optional
// — a nil mailer means auto-send is skipped; the PDF still
// lands in S3 and remains available via the download endpoint.
// Called from main.go after NewService.
func (s *Service) WithInvoiceMailer(m invoiceMailer) *Service {
	s.invoiceMailer = m
	return s
}

// sendInvoiceReadyMail delivers the "your invoice is ready" mail
// to the buyer with a link (not an attachment — modern SaaS
// billing practice, avoids TEM attachment complexity, and keeps
// deliverability high since the mail is small text/HTML). BCC
// billing@eurobase.app so we retain a copy for the accounting
// trail. Best-effort: failures log but don't roll back the
// underlying payment state.
//
// Attachment vs link decision: Estonian tax law requires the
// invoice to be MADE AVAILABLE to the buyer, not necessarily
// attached. A signed download link is the standard modern
// pattern (Stripe, Chargebee, Paddle all do this). If a buyer
// requires the attachment for their accounting, they can
// download and forward manually.
func (s *Service) sendInvoiceReadyMail(ctx context.Context, state invoiceEmailState) {
	if s.invoiceMailer == nil {
		return
	}
	if state.BuyerEmail == "" {
		return
	}

	subject := fmt.Sprintf("Eurobase invoice %s", state.InvoiceNumber)
	body := renderInvoiceMailBody(state)

	// Primary send to buyer.
	if err := s.invoiceMailer.SendRaw(ctx, state.BuyerEmail, subject, body); err != nil {
		slog.Warn("billing: invoice mail to buyer failed",
			"buyer", state.BuyerEmail,
			"invoice_number", state.InvoiceNumber,
			"error", err,
		)
		// Don't return — the accounting BCC still matters even
		// if the buyer mail bounced (proves we tried).
	}

	// Accounting copy. Separate send rather than a real BCC
	// header because TEM's REST body doesn't expose BCC — see
	// internal/email/client.go temRequest. A second SendRaw is
	// cheap.
	if err := s.invoiceMailer.SendRaw(ctx, accountingBCC, "[copy] "+subject, body); err != nil {
		slog.Warn("billing: invoice accounting copy failed",
			"invoice_number", state.InvoiceNumber, "error", err)
	}
}

// renderInvoiceMailBody renders the buyer-facing HTML. Same
// 600px inline-styled shape as the beta-update + downgrade
// mails; kept short — the invoice PDF is where the detail
// lives, this mail just tells the user it exists.
func renderInvoiceMailBody(s invoiceEmailState) string {
	amount := formatEUR(s.AmountCents, s.Currency)
	greeting := "Hi"
	if s.BuyerName != "" {
		greeting = "Hi " + s.BuyerName
	}
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<body style="margin:0;padding:0;background:#f3f4f6;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;">
  <table role="presentation" width="100%%" cellpadding="0" cellspacing="0" style="background:#f3f4f6;padding:24px 0;">
    <tr><td align="center">
      <table role="presentation" width="600" cellpadding="0" cellspacing="0" style="max-width:600px;background:#fff;border-radius:12px;">
        <tr><td style="background:#1d4ed8;padding:24px 32px;color:#fff;">
          <p style="margin:0;font-size:20px;font-weight:700;">Eurobase</p>
          <p style="margin:6px 0 0;font-size:14px;color:#bfdbfe;">Invoice %s</p>
        </td></tr>
        <tr><td style="padding:32px;color:#111827;">
          <p style="margin:0 0 16px;font-size:16px;">%s,</p>
          <p style="margin:0 0 20px;font-size:15px;line-height:1.6;color:#374151;">
            Your invoice for <strong>%s</strong> is ready.
          </p>
          <table role="presentation" width="100%%" style="border-collapse:collapse;margin:0 0 24px;">
            <tr>
              <td style="padding:8px 0;border-bottom:1px solid #e5e7eb;color:#6b7280;font-size:13px;">Invoice number</td>
              <td style="padding:8px 0;border-bottom:1px solid #e5e7eb;text-align:right;font-family:ui-monospace,Menlo,monospace;">%s</td>
            </tr>
            <tr>
              <td style="padding:8px 0;border-bottom:1px solid #e5e7eb;color:#6b7280;font-size:13px;">Amount</td>
              <td style="padding:8px 0;border-bottom:1px solid #e5e7eb;text-align:right;font-weight:600;">%s</td>
            </tr>
          </table>
          <p style="margin:0 0 20px;text-align:center;">
            <a href="%s" style="display:inline-block;background:#1d4ed8;color:#fff;font-weight:600;padding:12px 24px;border-radius:8px;text-decoration:none;">Download PDF invoice &rarr;</a>
          </p>
          <p style="margin:0 0 16px;font-size:14px;line-height:1.6;color:#374151;">
            The PDF is available on the <a href="%s" style="color:#1d4ed8;">billing page</a> of your project at any time. Nothing else to do.
          </p>
          <p style="margin:24px 0 6px;font-size:14px;color:#374151;">Thanks,<br><strong>Eurobase</strong></p>
        </td></tr>
        <tr><td style="padding:20px 32px 28px;border-top:1px solid #e5e7eb;">
          <p style="margin:0;font-size:12px;color:#9ca3af;">
            Eurobase O&Uuml; · Ahtri 12, Tallinn 15551, Estonia · Registry code 17557586<br>
            Not VAT-registered under Estonian VAT Act &sect;19.
          </p>
        </td></tr>
      </table>
    </td></tr>
  </table>
</body>
</html>`, s.InvoiceNumber, greeting, s.ProjectName, s.InvoiceNumber, amount, s.ConsoleBillingURL, s.ConsoleBillingURL)
}
