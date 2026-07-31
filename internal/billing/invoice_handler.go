package billing

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/go-chi/chi/v5"
)

// invoiceListItem is the JSON shape returned by
// GET /platform/billing/invoices. Kept flat so a table view in
// the console needs no client-side denormalisation.
type invoiceListItem struct {
	ID             string  `json:"id"`
	Number         string  `json:"number"`
	ProjectID      string  `json:"project_id"`
	ProjectName    string  `json:"project_name"`
	CreatedAt      string  `json:"created_at"`
	PaidAt         *string `json:"paid_at,omitempty"`
	AmountCents    int     `json:"amount_cents"`
	Currency       string  `json:"currency"`
	Status         string  `json:"status"`
	HasPDF         bool    `json:"has_pdf"`
}

// HandleListInvoices is GET /platform/billing/invoices. Returns
// every invoice for every project owned by the authenticated
// user, most-recent first. Auth-only.
func HandleListInvoices(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !svc.Enabled() {
			writeJSONError(w, http.StatusServiceUnavailable, "billing_disabled", "billing is not enabled in this environment")
			return
		}
		claims, ok := auth.ClaimsFromContext(r.Context())
		if !ok || claims == nil {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}

		rows, err := svc.pool.Query(r.Context(),
			`SELECT i.id, i.project_id, p.name, i.created_at, i.paid_at,
			        i.amount_cents, i.currency, i.status, i.pdf_object_key
			   FROM public.invoices i
			   JOIN public.projects p ON p.id = i.project_id
			  WHERE p.owner_id = $1::uuid
			  ORDER BY i.created_at DESC
			  LIMIT 500`,
			claims.Subject,
		)
		if err != nil {
			slog.Error("billing: list invoices query failed", "user_id", claims.Subject, "error", err)
			writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to load invoices")
			return
		}
		defer rows.Close()

		out := make([]invoiceListItem, 0, 32)
		for rows.Next() {
			var (
				it        invoiceListItem
				createdAt time.Time
				paidAt    *time.Time
				pdfKey    *string
			)
			if err := rows.Scan(&it.ID, &it.ProjectID, &it.ProjectName,
				&createdAt, &paidAt, &it.AmountCents, &it.Currency,
				&it.Status, &pdfKey); err != nil {
				slog.Error("billing: list invoices scan failed", "error", err)
				writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to read invoices")
				return
			}
			it.CreatedAt = createdAt.Format(time.RFC3339)
			if paidAt != nil {
				s := paidAt.Format(time.RFC3339)
				it.PaidAt = &s
			}
			it.Number = shortInvoiceNumber(it.ID)
			it.HasPDF = pdfKey != nil && *pdfKey != ""
			out = append(out, it)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"invoices": out})
	}
}

// HandleDownloadInvoicePDF is GET /platform/billing/invoices/:id/pdf.
// Verifies the invoice belongs to a project the caller owns, then
// 302s to a presigned S3 URL (5-min TTL). Two graceful
// fallbacks:
//
//   - If the invoice has no pdf_object_key (async render hasn't
//     completed yet), render on-demand and continue.
//   - If S3 isn't wired at all (dev environment), return 503.
func HandleDownloadInvoicePDF(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !svc.Enabled() {
			writeJSONError(w, http.StatusServiceUnavailable, "billing_disabled", "billing is not enabled in this environment")
			return
		}
		claims, ok := auth.ClaimsFromContext(r.Context())
		if !ok || claims == nil {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		invoiceID := chi.URLParam(r, "id")
		if invoiceID == "" {
			writeJSONError(w, http.StatusBadRequest, "missing_invoice_id", "invoice id path parameter required")
			return
		}

		// Ownership check + fetch pdf_object_key in one query.
		var (
			pdfKey *string
			ownerMatch bool
		)
		err := svc.pool.QueryRow(r.Context(),
			`SELECT i.pdf_object_key, (p.owner_id = $2::uuid) AS owner_match
			   FROM public.invoices i
			   JOIN public.projects p ON p.id = i.project_id
			  WHERE i.id = $1`,
			invoiceID, claims.Subject,
		).Scan(&pdfKey, &ownerMatch)
		if err != nil {
			// Not-found and not-owned are indistinguishable
			// deliberately (same reasoning as HandleCreateCheckout).
			writeJSONError(w, http.StatusNotFound, "invoice_not_found", "invoice not found")
			return
		}
		if !ownerMatch {
			writeJSONError(w, http.StatusNotFound, "invoice_not_found", "invoice not found")
			return
		}

		if svc.storage == nil {
			writeJSONError(w, http.StatusServiceUnavailable, "storage_not_configured", "invoice PDFs are not available in this environment")
			return
		}

		key := ""
		if pdfKey != nil {
			key = *pdfKey
		}

		// On-demand render if the async worker hasn't set the key
		// yet (fresh subscription, PDF still baking).
		if key == "" {
			if err := svc.RenderAndUploadInvoice(r.Context(), invoiceID); err != nil {
				if errors.Is(err, ErrInvoiceNotFound) {
					writeJSONError(w, http.StatusNotFound, "invoice_not_found", "invoice not found")
					return
				}
				slog.Error("billing: on-demand invoice render failed",
					"invoice_id", invoiceID, "error", err)
				writeJSONError(w, http.StatusInternalServerError, "render_failed", "failed to render invoice")
				return
			}
			// Re-read the key we just wrote.
			if err := svc.pool.QueryRow(r.Context(),
				`SELECT pdf_object_key FROM public.invoices WHERE id = $1`,
				invoiceID,
			).Scan(&pdfKey); err != nil || pdfKey == nil {
				slog.Error("billing: pdf_object_key still nil after render",
					"invoice_id", invoiceID, "error", err)
				writeJSONError(w, http.StatusInternalServerError, "render_failed", "failed to render invoice")
				return
			}
			key = *pdfKey
		}

		// Presigned GET URL — 5 min is long enough for the
		// browser to fetch but short enough that a leaked URL
		// isn't a lasting problem.
		filename := shortInvoiceNumber(invoiceID) + ".pdf"
		url, err := svc.storage.GeneratePresignedDownloadURLAs(r.Context(),
			invoicesBucket, key, 5*time.Minute, filename)
		if err != nil {
			slog.Error("billing: presign invoice URL failed",
				"invoice_id", invoiceID, "key", key, "error", err)
			writeJSONError(w, http.StatusInternalServerError, "presign_failed", "failed to generate download URL")
			return
		}

		http.Redirect(w, r, url, http.StatusFound)
	}
}
