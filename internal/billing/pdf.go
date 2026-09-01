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

	// Buyer — the platform user who owns the project. Email and
	// display name are always populated. The other Buyer* fields
	// come from public.billing_profiles when present and satisfy
	// Estonian VAT Act §37 + Accounting Act invoice minimums.
	// Empty strings on the profile fields mean "no billing profile
	// on file" (legacy invoices issued before migration 000106);
	// the renderer falls back to the email + display line and logs
	// a warning so ops can spot it.
	BuyerEmail         string
	BuyerDisplayName   string
	BuyerEntityType    string // 'individual' | 'business' | ""
	BuyerLegalName     string
	BuyerStreetAddress string
	BuyerPostalCode    string
	BuyerCity          string
	BuyerCountry       string // ISO 3166-1 alpha-2
	BuyerRegistryCode  string
	BuyerVATNumber     string

	// Line item — Eurobase invoices have exactly one line
	// today: the subscription for the billing period.
	Description  string // e.g. "Eurobase Pro — MyProject"
	PeriodFrom   time.Time
	PeriodTo     time.Time
	AmountCents  int
	Currency     string // "EUR"

	// ProjectName is repeated separately from Description so
	// the invoice-ready mail can render "Your invoice for
	// <project>" without string-splitting Description.
	ProjectName string
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

	// UTF-8 → cp1252 translator (#411 follow-up on user report:
	// invoice PDFs rendered with mojibake glyphs for €, §, Ü, —).
	// PDF core fonts (Helvetica) only speak cp1252; passing a raw
	// Go string (UTF-8) writes the raw bytes, which the PDF then
	// mis-decodes as cp1252 producing e.g. "â‚¬" instead of "€".
	// The translator converts each character to its cp1252
	// codepoint when one exists. Every character used on this
	// invoice today (€ U+20AC, § U+00A7, Ü U+00DC, — U+2014) is
	// in cp1252, so no glyph is dropped. Bundling a UTF-8 TTF via
	// AddUTF8Font would be needed only for non-cp1252 scripts
	// (Cyrillic / CJK) — not on the roadmap.
	//
	// Every user-facing string below goes through tr(), including
	// ASCII-only literals — the wrap is a no-op for those but
	// keeps the pattern uniform so a future add-a-field patch
	// can't miss it.
	tr := pdf.UnicodeTranslatorFromDescriptor("")

	// ── Header ────────────────────────────────────────────────
	pdf.SetFont("Helvetica", "B", 20)
	pdf.SetTextColor(29, 78, 216) // brand blue (#1d4ed8)
	pdf.Cell(0, 12, tr("Eurobase"))
	pdf.Ln(10)

	pdf.SetFont("Helvetica", "", 10)
	pdf.SetTextColor(107, 114, 128) // grey
	pdf.Cell(0, 5, tr("Invoice"))
	pdf.Ln(15)

	// ── Two-column layout: invoice meta (left) + seller info (right) ──
	yStart := pdf.GetY()

	// Left: invoice metadata.
	pdf.SetTextColor(17, 24, 39) // near-black
	pdf.SetFont("Helvetica", "B", 11)
	pdf.Cell(90, 6, tr("Invoice number"))
	pdf.Ln(5)
	pdf.SetFont("Helvetica", "", 10)
	pdf.Cell(90, 5, tr(d.InvoiceNumber))
	pdf.Ln(8)

	pdf.SetFont("Helvetica", "B", 11)
	pdf.Cell(90, 6, tr("Issued"))
	pdf.Ln(5)
	pdf.SetFont("Helvetica", "", 10)
	pdf.Cell(90, 5, tr(d.IssuedAt.Format("2 January 2006")))
	pdf.Ln(8)

	if !d.PaidAt.IsZero() {
		pdf.SetFont("Helvetica", "B", 11)
		pdf.Cell(90, 6, tr("Paid"))
		pdf.Ln(5)
		pdf.SetFont("Helvetica", "", 10)
		pdf.Cell(90, 5, tr(d.PaidAt.Format("2 January 2006")))
		pdf.Ln(8)
	}

	// Right: seller.
	pdf.SetY(yStart)
	pdf.SetX(110)
	pdf.SetFont("Helvetica", "B", 11)
	pdf.Cell(80, 6, tr("Seller"))
	pdf.Ln(5)
	pdf.SetX(110)
	pdf.SetFont("Helvetica", "", 10)
	pdf.Cell(80, 5, tr(d.SellerLegalName))
	pdf.Ln(5)
	pdf.SetX(110)
	pdf.Cell(80, 5, tr(d.SellerAddress))
	pdf.Ln(5)
	pdf.SetX(110)
	pdf.Cell(80, 5, tr("Estonia"))
	pdf.Ln(5)
	pdf.SetX(110)
	pdf.Cell(80, 5, tr("Registry code: "+d.SellerRegistryCode))
	pdf.Ln(5)
	pdf.SetX(110)
	pdf.Cell(80, 5, tr(d.SellerEmail))
	pdf.Ln(10)

	// ── Buyer ──────────────────────────────────────────────────
	pdf.Ln(6)
	pdf.SetFont("Helvetica", "B", 11)
	buyerLabel := "Buyer"
	if d.BuyerEntityType == "business" {
		buyerLabel = "Bill to"
	}
	pdf.Cell(0, 6, tr(buyerLabel))
	pdf.Ln(5)
	pdf.SetFont("Helvetica", "", 10)
	if d.BuyerLegalName != "" {
		// Full compliant billing block: legal name, street, postal
		// + city, country, optional registry code + VAT number,
		// then contact email as the last line.
		pdf.Cell(0, 5, tr(d.BuyerLegalName))
		pdf.Ln(5)
		pdf.Cell(0, 5, tr(d.BuyerStreetAddress))
		pdf.Ln(5)
		pdf.Cell(0, 5, tr(d.BuyerPostalCode+" "+d.BuyerCity))
		pdf.Ln(5)
		if d.BuyerCountry != "" {
			pdf.Cell(0, 5, tr(d.BuyerCountry))
			pdf.Ln(5)
		}
		if d.BuyerRegistryCode != "" {
			pdf.Cell(0, 5, tr("Registry code: "+d.BuyerRegistryCode))
			pdf.Ln(5)
		}
		if d.BuyerVATNumber != "" {
			pdf.Cell(0, 5, tr("VAT: "+d.BuyerVATNumber))
			pdf.Ln(5)
		}
		pdf.Cell(0, 5, tr(d.BuyerEmail))
		pdf.Ln(15)
	} else {
		// Legacy fallback for invoices issued before migration
		// 000106 (or if a profile was somehow deleted). Renders
		// the pre-profile shape so the PDF still comes out.
		// invoice_render.go logs a warning on this path so ops
		// can spot the drift.
		if d.BuyerDisplayName != "" {
			pdf.Cell(0, 5, tr(d.BuyerDisplayName))
			pdf.Ln(5)
		}
		pdf.Cell(0, 5, tr(d.BuyerEmail))
		pdf.Ln(15)
	}

	// ── Line items table ───────────────────────────────────────
	// Column widths sum to 170mm — the A4 usable width after the
	// 20mm side margins from SetMargins(20, 20, 20). Previous
	// allocation (110/30/30) left the Period column too narrow:
	// "24 Aug–24 Sep 2026" at 10pt Helvetica needs ~40mm, so the
	// year overflowed into the Amount column border (user-reported
	// 2026-08-24). Rebalanced: Description 100 / Period 45 / Amount
	// 25 — Period fits any single-month or spans-a-year range with
	// slack; Amount fits €25.00 through €9999.99 at 10pt.
	pdf.SetFont("Helvetica", "B", 10)
	pdf.SetFillColor(243, 244, 246) // light grey (#f3f4f6)
	pdf.CellFormat(100, 8, tr("Description"), "1", 0, "L", true, 0, "")
	pdf.CellFormat(45, 8, tr("Period"), "1", 0, "L", true, 0, "")
	pdf.CellFormat(25, 8, tr("Amount"), "1", 0, "R", true, 0, "")
	pdf.Ln(-1)

	pdf.SetFont("Helvetica", "", 10)
	pdf.CellFormat(100, 8, tr(d.Description), "1", 0, "L", false, 0, "")
	period := fmt.Sprintf("%s–%s", d.PeriodFrom.Format("2 Jan"), d.PeriodTo.Format("2 Jan 2006"))
	pdf.CellFormat(45, 8, tr(period), "1", 0, "L", false, 0, "")
	pdf.CellFormat(25, 8, tr(formatEUR(d.AmountCents, d.Currency)), "1", 0, "R", false, 0, "")
	pdf.Ln(-1)

	// Total row — label spans Description+Period columns (145mm),
	// value cell matches the Amount column (25mm) so the right
	// border lines up. 145+25 = 170 mm total, same as the header
	// row above.
	pdf.SetFont("Helvetica", "B", 11)
	pdf.CellFormat(145, 10, tr("Total"), "1", 0, "R", false, 0, "")
	pdf.CellFormat(25, 10, tr(formatEUR(d.AmountCents, d.Currency)), "1", 0, "R", false, 0, "")
	pdf.Ln(15)

	// ── VAT statement ──────────────────────────────────────────
	pdf.SetFont("Helvetica", "I", 9)
	pdf.SetTextColor(107, 114, 128)
	pdf.MultiCell(0, 5, tr(d.SellerVATNote), "", "L", false)
	pdf.Ln(8)

	// ── Footer (bottom of page) ────────────────────────────────
	pdf.SetY(-30)
	pdf.SetFont("Helvetica", "", 8)
	pdf.SetTextColor(156, 163, 175)
	pdf.CellFormat(0, 4, tr(d.SellerLegalName+" · "+d.SellerAddress+", Estonia"), "", 1, "C", false, 0, "")
	pdf.CellFormat(0, 4, tr("eurobase.app · "+d.SellerEmail), "", 1, "C", false, 0, "")

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
