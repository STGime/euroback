package billing

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/eurobase/euroback/internal/storage"
	"github.com/jackc/pgx/v5"
)

// invoicesBucket is the Scaleway Object Storage bucket that holds
// every rendered invoice PDF. Not created automatically — an ops
// script (`deploy/scripts/create-invoices-bucket.sh`) runs once
// per environment. See docs/billing/invoicing.md.
const invoicesBucket = "eurobase-platform-invoices"

// invoiceStorage is the subset of storage.S3Client that the
// renderer needs. Kept as an interface so tests can inject a fake
// that captures the uploaded bytes for assertions.
type invoiceStorage interface {
	UploadObject(ctx context.Context, bucket, key string, body []byte, contentType string) error
	GeneratePresignedDownloadURLAs(ctx context.Context, bucket, key string, expiry time.Duration, suggestedFilename string) (string, error)
}

// s3Adapter wraps the concrete storage.S3Client to fit
// invoiceStorage. UploadObject on the real client takes an
// io.Reader + size; here we take []byte because the PDF renderer
// produces the whole buffer anyway and the wrapping is trivial.
type s3Adapter struct{ c *storage.S3Client }

func (a s3Adapter) UploadObject(ctx context.Context, bucket, key string, body []byte, contentType string) error {
	return a.c.UploadObject(ctx, bucket, key, bytes.NewReader(body), contentType, int64(len(body)))
}

func (a s3Adapter) GeneratePresignedDownloadURLAs(ctx context.Context, bucket, key string, expiry time.Duration, suggestedFilename string) (string, error) {
	return a.c.GeneratePresignedDownloadURLAs(ctx, bucket, key, expiry, suggestedFilename)
}

// WithStorage attaches the S3 client. Called after NewService in
// main.go. Nil-safe — RenderInvoice returns ErrStorageNotConfigured
// when the client isn't wired (dev environments without S3 creds).
func (s *Service) WithStorage(s3 *storage.S3Client) *Service {
	if s3 != nil {
		s.storage = s3Adapter{c: s3}
	}
	return s
}

// ErrStorageNotConfigured is returned by invoice-render paths when
// the S3 client wasn't wired at startup. Handlers translate to
// 503; the webhook enqueue path just logs (a missing PDF isn't
// user-blocking — the console shows "not yet available").
var ErrStorageNotConfigured = errors.New("billing: object storage not configured")

// RenderAndUploadInvoice loads the invoice row + everything needed
// for the PDF, renders it, uploads to Scaleway, and writes
// pdf_object_key back to the row. Idempotent — a second call on
// an invoice that already has pdf_object_key re-renders and
// overwrites, which is fine (deterministic input → deterministic
// bytes) but wasteful. Callers should check pdf_object_key first
// when they can.
func (s *Service) RenderAndUploadInvoice(ctx context.Context, invoiceID string) error {
	if s.storage == nil {
		return ErrStorageNotConfigured
	}

	data, key, err := s.loadInvoiceData(ctx, invoiceID)
	if err != nil {
		return err
	}

	pdf, err := RenderInvoicePDF(data)
	if err != nil {
		return fmt.Errorf("render invoice %s: %w", invoiceID, err)
	}

	if err := s.storage.UploadObject(ctx, invoicesBucket, key, pdf, "application/pdf"); err != nil {
		return fmt.Errorf("upload invoice %s: %w", invoiceID, err)
	}

	if _, err := s.pool.Exec(ctx,
		`UPDATE public.invoices SET pdf_object_key = $1 WHERE id = $2`,
		key, invoiceID,
	); err != nil {
		// The bytes are already in S3; on the next render we'll
		// overwrite, and the DB pointer will be set then.
		return fmt.Errorf("persist pdf_object_key for %s: %w", invoiceID, err)
	}

	slog.Info("billing: invoice PDF rendered",
		"invoice_id", invoiceID,
		"key", key,
		"bytes", len(pdf),
	)

	// Fire the buyer notification + accounting BCC once the PDF
	// is actually retrievable. Guarded on paidAt to avoid mailing
	// on pre-render for unpaid rows (the async path fires from
	// the paid-transition webhook, but the on-demand path can be
	// hit against an unpaid invoice via the download endpoint).
	if !data.PaidAt.IsZero() {
		// Atomic claim-then-send. Two concurrent
		// RenderAndUploadInvoice calls (async webhook +
		// on-demand download) can both reach this point for
		// the same paid invoice; without this UPDATE both
		// would fire buyer + BCC mails. The UPDATE only
		// affects a row where invoice_mail_sent_at IS NULL,
		// so the SECOND caller sees RowsAffected == 0 and
		// silently skips the send.
		claim, cerr := s.pool.Exec(ctx,
			`UPDATE public.invoices
			    SET invoice_mail_sent_at = now()
			  WHERE id = $1
			    AND invoice_mail_sent_at IS NULL`,
			invoiceID,
		)
		if cerr != nil {
			slog.Warn("billing: mail-claim update failed — skipping mail",
				"invoice_id", invoiceID, "error", cerr)
		} else if claim.RowsAffected() > 0 {
			s.sendInvoiceReadyMail(ctx, invoiceEmailState{
				InvoiceNumber:     data.InvoiceNumber,
				ProjectName:       data.ProjectName,
				AmountCents:       data.AmountCents,
				Currency:          data.Currency,
				BuyerEmail:        data.BuyerEmail,
				BuyerName:         data.BuyerDisplayName,
				ConsoleBillingURL: s.config.ConsoleBaseURL + "/billing",
			})
		}
	}
	return nil
}

// enqueueInvoiceRender fires a goroutine that renders the invoice
// PDF in the background. Called from PR 4's webhook after the
// tx that flips a payment to 'paid' commits — the PDF isn't
// user-blocking (Mollie response is already 200'd), and a render
// failure logs but doesn't roll back the payment state.
//
// Deliberately not a River job. River gives us retries + a
// visible failure queue, both of which are useful — but the
// dependency footprint (a whole River worker registration, plus
// wiring the billing service into cmd/worker) doubles the moving
// parts of this PR for a background task that's easily
// re-triggered by the download endpoint's on-demand fallback.
func (s *Service) enqueueInvoiceRender(invoiceID string) {
	if s.storage == nil {
		// Dev mode without S3 — the download endpoint's
		// on-demand render also short-circuits, so this is
		// harmless.
		return
	}
	go func() {
		// Detached context — the incoming HTTP context may be
		// canceled by Mollie's client after 200, and we don't
		// want that to abort the render.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := s.RenderAndUploadInvoice(ctx, invoiceID); err != nil {
			slog.Error("billing.invoice.render_failed",
				"invoice_id", invoiceID,
				"error", err,
			)
		}
	}()
}

// loadInvoiceData JOINs invoices → subscriptions → projects →
// platform_users to build the InvoiceData struct the renderer
// needs. The key is derived from invoice ID so uploads are
// content-idempotent within an invoice — a second render for
// the same invoice overwrites cleanly.
func (s *Service) loadInvoiceData(ctx context.Context, invoiceID string) (InvoiceData, string, error) {
	var (
		invoiceCreatedAt time.Time
		paidAt           *time.Time
		amountCents      int
		currency         string
		invoiceNumber    int64
		periodStart      *time.Time
		periodEnd        *time.Time
		planCode         string
		projectName      string
		ownerEmail       string
		ownerDisplayName *string
		// Billing profile fields (migration 000106). NULL columns
		// on the LEFT JOIN mean "no profile on file" — the
		// renderer's legacy fallback kicks in and we log a warning.
		bpEntityType    *string
		bpLegalName     *string
		bpStreetAddress *string
		bpPostalCode    *string
		bpCity          *string
		bpCountry       *string
		bpRegistryCode  *string
		bpVATNumber     *string
	)
	// Prefer the invoice.subscription_id link (migration 000081)
	// so a re-render always resolves to the SAME subscription the
	// invoice was originally issued for. LEFT JOIN falls back to
	// "any subscription for this project" only for pre-000081
	// historical rows where subscription_id may still be NULL.
	//
	// Runs on the developer pool (s.pii()) because the JOIN pulls
	// public.billing_profiles, which 000106 REVOKEs from gateway.
	// The other JOINed tables (invoices / projects / platform_users
	// / subscriptions) still allow gateway access, so promoting
	// the whole query to developer is strictly a widening of
	// privilege for those reads — acceptable because this is a
	// platform-authenticated developer-traffic path (console
	// downloading its own invoice), not SDK runtime traffic.
	err := s.pii().QueryRow(ctx,
		`SELECT i.created_at, i.paid_at, i.amount_cents, i.currency,
		        i.invoice_number,
		        s.started_at, s.next_charge_at, s.plan,
		        p.name, u.email, u.display_name,
		        bp.entity_type, bp.legal_name, bp.street_address,
		        bp.postal_code, bp.city, bp.country,
		        bp.registry_code, bp.vat_number
		   FROM public.invoices i
		   JOIN public.projects p ON p.id = i.project_id
		   JOIN public.platform_users u ON u.id = p.owner_id
		   LEFT JOIN public.billing_profiles bp ON bp.platform_user_id = u.id
		   LEFT JOIN public.subscriptions s ON s.id = COALESCE(
		          i.subscription_id,
		          (SELECT id FROM public.subscriptions
		            WHERE project_id = i.project_id
		              AND status IN ('active', 'past_due', 'expired', 'canceled', 'incomplete')
		            ORDER BY started_at ASC NULLS LAST
		            LIMIT 1)
		   )
		  WHERE i.id = $1`,
		invoiceID,
	).Scan(&invoiceCreatedAt, &paidAt, &amountCents, &currency,
		&invoiceNumber,
		&periodStart, &periodEnd, &planCode,
		&projectName, &ownerEmail, &ownerDisplayName,
		&bpEntityType, &bpLegalName, &bpStreetAddress,
		&bpPostalCode, &bpCity, &bpCountry,
		&bpRegistryCode, &bpVATNumber)
	if errors.Is(err, pgx.ErrNoRows) {
		return InvoiceData{}, "", fmt.Errorf("invoice %s: %w", invoiceID, ErrInvoiceNotFound)
	}
	if err != nil {
		return InvoiceData{}, "", fmt.Errorf("load invoice %s: %w", invoiceID, err)
	}

	// Fall back to sensible defaults for missing sub linkage
	// (edge case: invoice for a canceled sub whose row was
	// pruned — shouldn't happen with our current schema but the
	// LEFT JOIN keeps rendering possible if it does).
	if periodStart == nil {
		p := invoiceCreatedAt
		periodStart = &p
	}
	if periodEnd == nil {
		p := invoiceCreatedAt.AddDate(0, 1, 0)
		periodEnd = &p
	}
	if planCode == "" {
		planCode = "pro"
	}

	displayName := ""
	if ownerDisplayName != nil {
		displayName = *ownerDisplayName
	}
	paidAtVal := time.Time{}
	if paidAt != nil {
		paidAtVal = *paidAt
	}

	data := InvoiceData{
		InvoiceNumber:      formatInvoiceNumber(invoiceCreatedAt.Year(), invoiceNumber),
		IssuedAt:           invoiceCreatedAt,
		PaidAt:             paidAtVal,
		SellerLegalName:    "Eurobase OÜ",
		SellerAddress:      "Ahtri 12, Tallinn 15551",
		SellerRegistryCode: "17557586",
		SellerVATNote:      "Not VAT-registered under Estonian VAT Act §19 (below the €40,000 taxable-turnover threshold).",
		SellerEmail:        "contact@eurobase.app",
		BuyerEmail:         ownerEmail,
		BuyerDisplayName:   displayName,
		Description:        fmt.Sprintf("Eurobase %s subscription — %s", planCode, projectName),
		PeriodFrom:         *periodStart,
		PeriodTo:           *periodEnd,
		AmountCents:        amountCents,
		Currency:           currency,
		ProjectName:        projectName,
	}

	// Overlay billing-profile fields when present. bpLegalName is
	// the sentinel — every row that has a profile has all of
	// entity_type/legal_name/street/postal/city/country populated
	// (DB CHECK constraints on migration 000106). Missing profile
	// leaves the fields empty; the renderer's legacy fallback
	// takes over. Log a warning so ops sees any invoice issued
	// after the launch-flip that somehow slipped past the
	// checkout gate — should not happen once BILLING_PROFILE_REQUIRED
	// is live in prod.
	if bpLegalName != nil {
		data.BuyerEntityType = strPtrOr(bpEntityType, "")
		data.BuyerLegalName = *bpLegalName
		data.BuyerStreetAddress = strPtrOr(bpStreetAddress, "")
		data.BuyerPostalCode = strPtrOr(bpPostalCode, "")
		data.BuyerCity = strPtrOr(bpCity, "")
		data.BuyerCountry = strPtrOr(bpCountry, "")
		data.BuyerRegistryCode = strPtrOr(bpRegistryCode, "")
		data.BuyerVATNumber = strPtrOr(bpVATNumber, "")
	} else {
		slog.Warn("billing: invoice rendered without billing profile — legacy fallback path",
			"invoice_id", invoiceID,
			"owner_email", ownerEmail,
		)
	}

	// Object key derived from the invoice UUID. Grouped by first
	// two hex chars for a wider fan-out under S3's key hash — a
	// bucket with N invoices distributes across up to 256
	// prefixes, which matters when N crosses ~100k.
	key := "invoices/" + invoiceID[:2] + "/" + invoiceID + ".pdf"
	return data, key, nil
}

// ErrInvoiceNotFound is returned when an invoice UUID doesn't
// exist. Handlers translate to 404.
var ErrInvoiceNotFound = errors.New("billing: invoice not found")

// strPtrOr dereferences a nullable string column, returning
// fallback when the pointer is nil.
func strPtrOr(s *string, fallback string) string {
	if s == nil {
		return fallback
	}
	return *s
}

// formatInvoiceNumber renders the sequence + year as
// EB-YYYY-NNNNNN. Year comes from the invoice's created_at so
// re-rendering a historical invoice never drifts. The 6-digit
// zero-pad handles up to a million invoices — well beyond
// what beta needs and simple to widen later if we ever hit
// it. The tax-audit-friendly property is the monotonic
// sequence, not the format string.
func formatInvoiceNumber(year int, seq int64) string {
	return fmt.Sprintf("EB-%04d-%06d", year, seq)
}
