package billing

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// HandleCheckout starts a Mollie checkout for the project's owner.
// POST /platform/projects/{id}/billing/checkout
// Body: { "plan": "pro" }
// Returns: { "checkout_url": "..." }
func HandleCheckout(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil || !svc.mollie.Configured() {
			writeBillingJSON(w, map[string]string{"error": "billing not configured"}, http.StatusServiceUnavailable)
			return
		}
		projectID := chi.URLParam(r, "id")
		claims, _ := auth.ClaimsFromContext(r.Context())
		if claims == nil {
			writeBillingJSON(w, map[string]string{"error": "unauthorized"}, http.StatusUnauthorized)
			return
		}

		var body struct {
			Plan string `json:"plan"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Plan == "" {
			body.Plan = PlanPro
		}

		// We need the project's owner identity to attach to Mollie.
		// The platform handler upstream is gated to admin role; the
		// owner is the natural billing actor.
		var ownerID, ownerEmail, ownerName string
		err := svc.pool.QueryRow(r.Context(),
			`SELECT pu.id, pu.email, COALESCE(pu.name, pu.email)
			 FROM public.projects p
			 JOIN public.platform_users pu ON pu.id = p.owner_id
			 WHERE p.id = $1`,
			projectID,
		).Scan(&ownerID, &ownerEmail, &ownerName)
		if err == pgx.ErrNoRows {
			writeBillingJSON(w, map[string]string{"error": "project not found"}, http.StatusNotFound)
			return
		}
		if err != nil {
			slog.Error("billing checkout: lookup owner failed", "project", projectID, "error", err)
			writeBillingJSON(w, map[string]string{"error": "internal error"}, http.StatusInternalServerError)
			return
		}

		checkoutURL, err := svc.StartUpgrade(r.Context(), projectID, ownerID, ownerEmail, ownerName, body.Plan)
		if err != nil {
			slog.Error("billing checkout failed", "project", projectID, "error", err)
			writeBillingJSON(w, map[string]string{"error": "checkout failed"}, http.StatusBadGateway)
			return
		}
		writeBillingJSON(w, map[string]string{"checkout_url": checkoutURL}, http.StatusOK)
	}
}

// HandleGetSubscription returns the project's current subscription
// state. Free projects with no row still get a valid response.
// GET /platform/projects/{id}/billing/subscription
func HandleGetSubscription(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		view, err := svc.GetSubscription(r.Context(), projectID)
		if err != nil {
			slog.Error("billing get subscription failed", "project", projectID, "error", err)
			writeBillingJSON(w, map[string]string{"error": "internal error"}, http.StatusInternalServerError)
			return
		}
		writeBillingJSON(w, view, http.StatusOK)
	}
}

// HandleCancel flags the subscription cancel-at-period-end + asks
// Mollie to stop further charges.
// POST /platform/projects/{id}/billing/cancel
func HandleCancel(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil || !svc.mollie.Configured() {
			writeBillingJSON(w, map[string]string{"error": "billing not configured"}, http.StatusServiceUnavailable)
			return
		}
		projectID := chi.URLParam(r, "id")
		claims, _ := auth.ClaimsFromContext(r.Context())
		actorID, actorEmail := "", ""
		if claims != nil {
			actorID = claims.Subject
			actorEmail = claims.Email
		}
		if err := svc.Cancel(r.Context(), projectID, actorID, actorEmail); err != nil {
			slog.Error("billing cancel failed", "project", projectID, "error", err)
			writeBillingJSON(w, map[string]string{"error": err.Error()}, http.StatusBadRequest)
			return
		}
		writeBillingJSON(w, map[string]string{"status": "cancelled_at_period_end"}, http.StatusOK)
	}
}

// InvoiceView mirrors the columns the console renders.
type InvoiceView struct {
	ID              string `json:"id"`
	MolliePaymentID string `json:"mollie_payment_id"`
	AmountCents     int64  `json:"amount_cents"`
	Currency        string `json:"currency"`
	Status          string `json:"status"`
	PaidAt          string `json:"paid_at,omitempty"`
	CreatedAt       string `json:"created_at"`
}

// HandleListInvoices returns the project's invoices, newest first.
// GET /platform/projects/{id}/billing/invoices
func HandleListInvoices(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		rows, err := svc.pool.Query(r.Context(),
			`SELECT id::text, mollie_payment_id, amount_cents, currency,
			        status, COALESCE(paid_at::text, ''), created_at::text
			 FROM public.invoices
			 WHERE project_id = $1
			 ORDER BY created_at DESC
			 LIMIT 20`,
			projectID,
		)
		if err != nil {
			slog.Error("billing list invoices failed", "project", projectID, "error", err)
			writeBillingJSON(w, map[string]string{"error": "internal error"}, http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		out := []InvoiceView{}
		for rows.Next() {
			var inv InvoiceView
			if err := rows.Scan(&inv.ID, &inv.MolliePaymentID, &inv.AmountCents,
				&inv.Currency, &inv.Status, &inv.PaidAt, &inv.CreatedAt); err != nil {
				continue
			}
			out = append(out, inv)
		}
		writeBillingJSON(w, map[string]any{"invoices": out}, http.StatusOK)
	}
}

// HandleWebhook is the Mollie webhook receiver. Unauthenticated by
// design — Mollie can't carry our JWT — and protected by:
// (a) HMAC signature verification using MOLLIE_WEBHOOK_SECRET
// (b) a fetch of the payment from Mollie's authoritative API
// (c) idempotent state transitions (ON CONFLICT DO NOTHING +
//     status-machine guards in the service layer)
//
// We always return 200 to Mollie regardless of internal error.
// Returning non-2xx triggers retries indefinitely; we never want a
// poison-pill webhook stuck in retry loop. Internal failures are
// logged + audit-trailed instead.
//
// POST /webhooks/mollie
// Content-Type: application/x-www-form-urlencoded
// Body: id=tr_xxxxxxxx
// Header: X-Mollie-Signature: <hex sha256>
func HandleWebhook(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil || !svc.mollie.Configured() {
			// Always 200 so Mollie doesn't retry; log loudly.
			slog.Error("mollie webhook received but billing not configured")
			w.WriteHeader(http.StatusOK)
			return
		}

		raw, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
		if err != nil {
			slog.Error("mollie webhook: read body failed", "error", err)
			w.WriteHeader(http.StatusOK)
			return
		}

		sig := r.Header.Get("X-Mollie-Signature")
		if err := svc.mollie.VerifyWebhookSignature(raw, sig); err != nil {
			slog.Warn("mollie webhook signature invalid", "error", err, "sig_present", sig != "")
			// Still 200 — signature failures should NOT cause Mollie
			// to retry. The wrong-secret case is operator error,
			// not transient.
			w.WriteHeader(http.StatusOK)
			return
		}

		// Mollie sends payment IDs as form data: id=tr_xxxxxxxx.
		// We already drained r.Body into `raw` for signature
		// verification, so `r.ParseForm()` reads an empty body and
		// returns nothing — that was a real bug caught in PR #163
		// review. Parse the already-read bytes instead.
		paymentID := ""
		if r.Header.Get("Content-Type") == "application/x-www-form-urlencoded" {
			if values, err := url.ParseQuery(string(raw)); err == nil {
				paymentID = values.Get("id")
			}
		}
		if paymentID == "" {
			// Some Mollie integration tooling posts JSON; tolerate.
			var jsonBody struct{ ID string `json:"id"` }
			_ = json.Unmarshal(raw, &jsonBody)
			paymentID = jsonBody.ID
		}
		if paymentID == "" {
			slog.Warn("mollie webhook: no payment ID found in body")
			w.WriteHeader(http.StatusOK)
			return
		}

		if err := svc.ApplyPaymentEvent(r.Context(), paymentID); err != nil {
			slog.Error("mollie webhook: apply payment event failed",
				"payment_id", paymentID, "error", err)
		}
		w.WriteHeader(http.StatusOK)
	}
}

func writeBillingJSON(w http.ResponseWriter, data any, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

// pingDBRoundtrip is a no-op left in for future health-check use.
// Kept here (rather than inlined) so dunning + handler share the
// same package surface and the imports stay minimal.
var _ = context.Background