package billing

import (
	"bytes"
	"fmt"
	"time"

	"github.com/go-pdf/fpdf"
)

// InvoiceData is the input to RenderInvoicePDF. Populated by the
// renderer from a JOIN across invoices / subscriptions / projects
// / platform_users so this struct is decoupled from the SQL shape.
//
// All money values are integer cents; PDF renders them as
// "€X.XX" via formatEUR. Keep timestamps in the invoice's local
// billing period rather than "now" so a re-render doesn't
// silently rewrite the paid_at line.
type InvoiceData struct {
	// InvoiceNumber is the human-facing identifier printed at the
	// top of the invoice. Derived from the invoice UUID's first
	// segment; short enough to quote over the phone.
	InvoiceNumber string
	IssuedAt      time.Time
	PaidAt        time.Time

	// Seller — Eurobase OÜ. Values sourced from legalStrings so
	// the invoice stays in sync with /legal on the marketing
	// site.
	SellerLegalName    string
	SellerAddress      string
	SellerRegistryCode string
	SellerVATNote      string // "Not VAT-registered (below Estonian €40,000 threshold)"
	SellerEmail        string

	// Buyer — the platform user who owns the project. We only
	// hold their email; a proper business address would come
	// from a future "billing profile" feature on the account.
	BuyerEmail       string
	BuyerDisplayName string

	// Line item — Eurobase invoices have exactly one line
	// today: the subscription for the billing period.
	Description  string // e.g. "Eurobase Pro — MyProject"
	PeriodFrom   time.Time
	PeriodTo     time.Time
	AmountCents  int
	Currency     string // "EUR"
}

// RenderInvoicePDF produces a PDF (A4, single page) matching the
// Estonian invoice minimum-content rules for a non-VAT-registered
// entity. Returns the raw PDF bytes so the caller can upload to
// Scaleway Object Storage without an intermediate temp file.
//
// The fpdf library is pure Go — no Chrome, no external binaries,
// no CGO. Trade-off: the visual polish ceiling is lower than
// chromedp/wkhtmltopdf, but for a one-line subscription invoice
// that's fine.
func RenderInvoicePDF(d InvoiceData) ([]byte, error) {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(20, 20, 20)
	pdf.AddPage()

	// ── Header ────────────────────────────────────────────────
	pdf.SetFont("Helvetica", "B", 20)
	pdf.SetTextColor(29, 78, 216) // brand blue (#1d4ed8)
	pdf.Cell(0, 12, "Eurobase")
	pdf.Ln(10)

	pdf.SetFont("Helvetica", "", 10)
	pdf.SetTextColor(107, 114, 128) // grey
	pdf.Cell(0, 5, "Invoice")
	pdf.Ln(15)

	// ── Two-column layout: invoice meta (left) + seller info (right) ──
	yStart := pdf.GetY()

	// Left: invoice metadata.
	pdf.SetTextColor(17, 24, 39) // near-black
	pdf.SetFont("Helvetica", "B", 11)
	pdf.Cell(90, 6, "Invoice number")
	pdf.Ln(5)
	pdf.SetFont("Helvetica", "", 10)
	pdf.Cell(90, 5, d.InvoiceNumber)
	pdf.Ln(8)

	pdf.SetFont("Helvetica", "B", 11)
	pdf.Cell(90, 6, "Issued")
	pdf.Ln(5)
	pdf.SetFont("Helvetica", "", 10)
	pdf.Cell(90, 5, d.IssuedAt.Format("2 January 2006"))
	pdf.Ln(8)

	if !d.PaidAt.IsZero() {
		pdf.SetFont("Helvetica", "B", 11)
		pdf.Cell(90, 6, "Paid")
		pdf.Ln(5)
		pdf.SetFont("Helvetica", "", 10)
		pdf.Cell(90, 5, d.PaidAt.Format("2 January 2006"))
		pdf.Ln(8)
	}

	// Right: seller.
	pdf.SetY(yStart)
	pdf.SetX(110)
	pdf.SetFont("Helvetica", "B", 11)
	pdf.Cell(80, 6, "Seller")
	pdf.Ln(5)
	pdf.SetX(110)
	pdf.SetFont("Helvetica", "", 10)
	pdf.Cell(80, 5, d.SellerLegalName)
	pdf.Ln(5)
	pdf.SetX(110)
	pdf.Cell(80, 5, d.SellerAddress)
	pdf.Ln(5)
	pdf.SetX(110)
	pdf.Cell(80, 5, "Estonia")
	pdf.Ln(5)
	pdf.SetX(110)
	pdf.Cell(80, 5, "Registry code: "+d.SellerRegistryCode)
	pdf.Ln(5)
	pdf.SetX(110)
	pdf.Cell(80, 5, d.SellerEmail)
	pdf.Ln(10)

	// ── Buyer ──────────────────────────────────────────────────
	pdf.Ln(6)
	pdf.SetFont("Helvetica", "B", 11)
	pdf.Cell(0, 6, "Buyer")
	pdf.Ln(5)
	pdf.SetFont("Helvetica", "", 10)
	if d.BuyerDisplayName != "" {
		pdf.Cell(0, 5, d.BuyerDisplayName)
		pdf.Ln(5)
	}
	pdf.Cell(0, 5, d.BuyerEmail)
	pdf.Ln(15)

	// ── Line items table ───────────────────────────────────────
	pdf.SetFont("Helvetica", "B", 10)
	pdf.SetFillColor(243, 244, 246) // light grey (#f3f4f6)
	pdf.CellFormat(110, 8, "Description", "1", 0, "L", true, 0, "")
	pdf.CellFormat(30, 8, "Period", "1", 0, "L", true, 0, "")
	pdf.CellFormat(30, 8, "Amount", "1", 0, "R", true, 0, "")
	pdf.Ln(-1)

	pdf.SetFont("Helvetica", "", 10)
	pdf.CellFormat(110, 8, d.Description, "1", 0, "L", false, 0, "")
	period := fmt.Sprintf("%s–%s", d.PeriodFrom.Format("2 Jan"), d.PeriodTo.Format("2 Jan 2006"))
	pdf.CellFormat(30, 8, period, "1", 0, "L", false, 0, "")
	pdf.CellFormat(30, 8, formatEUR(d.AmountCents, d.Currency), "1", 0, "R", false, 0, "")
	pdf.Ln(-1)

	// Total row.
	pdf.SetFont("Helvetica", "B", 11)
	pdf.CellFormat(140, 10, "Total", "1", 0, "R", false, 0, "")
	pdf.CellFormat(30, 10, formatEUR(d.AmountCents, d.Currency), "1", 0, "R", false, 0, "")
	pdf.Ln(15)

	// ── VAT statement ──────────────────────────────────────────
	pdf.SetFont("Helvetica", "I", 9)
	pdf.SetTextColor(107, 114, 128)
	pdf.MultiCell(0, 5, d.SellerVATNote, "", "L", false)
	pdf.Ln(8)

	// ── Footer (bottom of page) ────────────────────────────────
	pdf.SetY(-30)
	pdf.SetFont("Helvetica", "", 8)
	pdf.SetTextColor(156, 163, 175)
	pdf.CellFormat(0, 4, d.SellerLegalName+" · "+d.SellerAddress+", Estonia", "", 1, "C", false, 0, "")
	pdf.CellFormat(0, 4, "eurobase.app · "+d.SellerEmail, "", 1, "C", false, 0, "")

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, fmt.Errorf("render pdf: %w", err)
	}
	return buf.Bytes(), nil
}

// formatEUR renders integer cents as a locale-neutral money
// string (currency prefix + comma-less decimal). "EUR" prefixes
// with the € symbol; anything else falls back to the ISO code.
// Locale-neutral because invoices go to EU-wide customers and
// the German "1.900,00" vs UK "1,900.00" swap has caused real
// support tickets on other platforms.
func formatEUR(cents int, currency string) string {
	prefix := currency + " "
	if currency == "EUR" {
		prefix = "€"
	}
	if cents < 0 {
		cents = -cents
		prefix = "-" + prefix
	}
	whole := cents / 100
	frac := cents % 100
	return fmt.Sprintf("%s%d.%02d", prefix, whole, frac)
}
