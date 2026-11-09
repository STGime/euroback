package billing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/billing/mollie"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// purposePaymentMethodUpdate marks the zero-amount first payment that
// only sets up a new mandate for an existing subscription (metadata
// "purpose"). The webhook routes it to applyPaymentMethodUpdate instead
// of the checkout activation.
const purposePaymentMethodUpdate = "payment_method_update"

var (
	// ErrNotPayer: the project's subscription is paid by someone else
	// (a previous owner's subscription running out after a transfer).
	ErrNotPayer = errors.New("billing: the subscription is not paid by the caller")
	// ErrPaymentMethodNotChangeable: the subscription is cancelled at
	// period end or not set up at Mollie yet.
	ErrPaymentMethodNotChangeable = errors.New("billing: the payment method of this subscription can't be changed")
)

// payerSubscription is the project's live subscription as seen by its
// current owner.
type payerSubscription struct {
	ID              string
	ProjectName     string
	Status          string
	CanceledAt      *time.Time
	NextChargeAt    *time.Time
	MollieSubID     string
	MollieCustID    string
	OwnerCustomerID string
}

// loadPayerSubscription loads the project's live subscription for its
// owner and checks the owner is the one paying for it.
func (s *Service) loadPayerSubscription(ctx context.Context, userID, projectID string) (*payerSubscription, error) {
	var ps payerSubscription
	var mollieSub, mollieCust, ownerCust *string
	err := s.pool.QueryRow(ctx,
		`SELECT s.id, p.name, s.status, s.canceled_at, s.next_charge_at,
		        s.mollie_subscription_id, s.mollie_customer_id, u.mollie_customer_id
		   FROM public.subscriptions s
		   JOIN public.projects p ON p.id = s.project_id
		   JOIN public.platform_users u ON u.id = p.owner_id
		  WHERE s.project_id = $1
		    AND p.owner_id = $2::uuid
		    AND p.status = 'active'
		    AND s.status IN ('incomplete', 'active', 'past_due')
		  ORDER BY s.started_at DESC NULLS LAST
		  LIMIT 1`,
		projectID, userID,
	).Scan(&ps.ID, &ps.ProjectName, &ps.Status, &ps.CanceledAt, &ps.NextChargeAt, &mollieSub, &mollieCust, &ownerCust)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSubscriptionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("billing: load subscription: %w", err)
	}
	if mollieSub != nil {
		ps.MollieSubID = *mollieSub
	}
	if mollieCust != nil {
		ps.MollieCustID = *mollieCust
	}
	if ownerCust != nil {
		ps.OwnerCustomerID = *ownerCust
	}
	if ps.MollieCustID == "" || ps.MollieCustID != ps.OwnerCustomerID {
		return nil, ErrNotPayer
	}
	return &ps, nil
}

// PaymentMethodView is what the billing page shows about the mandate a
// subscription is charged to. Only the payer sees it.
type PaymentMethodView struct {
	// Method is creditcard, paypal or directdebit.
	Method string `json:"method"`
	// Status is valid, pending or invalid (Mollie's mandate status).
	Status     string `json:"status"`
	CardLabel  string `json:"card_label,omitempty"` // e.g. "Visa"
	CardLast4  string `json:"card_last4,omitempty"`
	CardExpiry string `json:"card_expiry,omitempty"` // YYYY-MM
	// AccountName / Account: PayPal name + email, or the bank account
	// holder + the IBAN's last four characters.
	AccountName string `json:"account_name,omitempty"`
	Account     string `json:"account,omitempty"`
	// ExpiresBeforeNextCharge: the card's expiry month ends before the
	// next charge date.
	ExpiresBeforeNextCharge bool `json:"expires_before_next_charge"`
	// CanChange: the owner can switch to another card / PayPal account
	// now (active, not cancelled at period end).
	CanChange bool `json:"can_change"`
}

// GetPaymentMethod returns the payment method the project's
// subscription is charged to.
func (s *Service) GetPaymentMethod(ctx context.Context, userID, projectID string) (*PaymentMethodView, error) {
	if !s.enabled {
		return nil, ErrBillingDisabled
	}
	ps, err := s.loadPayerSubscription(ctx, userID, projectID)
	if err != nil {
		return nil, err
	}
	if ps.MollieSubID == "" {
		return nil, ErrSubscriptionNotFound
	}
	sub, err := s.client.GetSubscription(ctx, ps.MollieCustID, ps.MollieSubID)
	if err != nil {
		return nil, fmt.Errorf("billing: fetch subscription: %w", err)
	}
	if sub.MandateID == "" {
		return nil, ErrSubscriptionNotFound
	}
	m, err := s.client.GetMandate(ctx, ps.MollieCustID, sub.MandateID)
	if err != nil {
		return nil, fmt.Errorf("billing: fetch mandate: %w", err)
	}
	v := mandateView(m, ps.NextChargeAt)
	v.CanChange = ps.Status == "active" && ps.CanceledAt == nil
	return v, nil
}

func mandateView(m *mollie.Mandate, nextCharge *time.Time) *PaymentMethodView {
	v := &PaymentMethodView{Method: m.Method, Status: m.Status}
	switch m.Method {
	case "creditcard":
		v.CardLabel = m.Details.CardLabel
		v.CardLast4 = m.Details.CardNumber
		if exp, err := time.Parse("2006-01-02", m.Details.CardExpiryDate); err == nil {
			v.CardExpiry = exp.Format("2006-01")
			// Valid through the end of the expiry month.
			endOfMonth := time.Date(exp.Year(), exp.Month()+1, 1, 0, 0, 0, 0, time.UTC)
			if nextCharge != nil && !nextCharge.Before(endOfMonth) {
				v.ExpiresBeforeNextCharge = true
			}
		}
	case "paypal":
		v.AccountName = m.Details.ConsumerName
		v.Account = m.Details.ConsumerAccount
	case "directdebit":
		v.AccountName = m.Details.ConsumerName
		if a := m.Details.ConsumerAccount; len(a) > 4 {
			v.Account = "•••• " + a[len(a)-4:]
		}
	}
	return v
}

// StartPaymentMethodUpdate creates a zero-amount first payment for the
// owner to add a new card or PayPal account. Nothing is charged; once it
// is paid the webhook moves the subscription to the new mandate
// (applyPaymentMethodUpdate). Returns the Mollie checkout URL.
//
// Past-due subscriptions are refused here: the owed charge has to be
// collected as well, which Pay now does (pay_now.go).
func (s *Service) StartPaymentMethodUpdate(ctx context.Context, userID, projectID string) (string, error) {
	if !s.enabled {
		return "", ErrBillingDisabled
	}
	ps, err := s.loadPayerSubscription(ctx, userID, projectID)
	if err != nil {
		return "", err
	}
	if ps.Status == "past_due" {
		return "", ErrPaymentOverdue
	}
	if ps.Status != "active" || ps.CanceledAt != nil || ps.MollieSubID == "" {
		return "", ErrPaymentMethodNotChangeable
	}

	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", fmt.Errorf("billing: nonce: %w", err)
	}
	payment, err := s.client.CreatePayment(ctx, mollie.PaymentCreateRequest{
		Amount:       mollie.AmountFromCents(0, "EUR"),
		Description:  fmt.Sprintf("Eurobase — new payment method for %s", ps.ProjectName),
		RedirectURL:  fmt.Sprintf("%s/p/%s/billing?status=payment_method", s.config.ConsoleBaseURL, projectID),
		WebhookURL:   fmt.Sprintf("%s/platform/billing/webhook", s.config.WebhookBaseURL),
		CustomerID:   ps.MollieCustID,
		SequenceType: mollie.SequenceTypeFirst,
		Method:       mollie.MandateMethods,
		Metadata: map[string]string{
			"purpose":         purposePaymentMethodUpdate,
			"subscription_id": ps.ID,
			"project_id":      projectID,
		},
	}, mollie.WithIdempotencyKey("pm-update:"+ps.ID+":"+hex.EncodeToString(nonce[:])))
	if err != nil {
		return "", fmt.Errorf("billing: create mollie payment: %w", err)
	}
	if payment.Links.Checkout == nil || payment.Links.Checkout.Href == "" {
		return "", fmt.Errorf("billing: mollie returned no checkout URL (payment %s)", payment.ID)
	}
	slog.Info("billing: payment method update started",
		"subscription_id", ps.ID, "project_id", projectID, "mollie_payment_id", payment.ID)
	return payment.Links.Checkout.Href, nil
}

// applyPaymentMethodUpdate runs when a payment-method-update payment is
// paid: it moves the Mollie subscription to the mandate that payment
// created. The old mandate is left in place: revoking a mandate cancels
// every subscription connected to it, and Mollie may share one card's
// mandate between the owner's projects.
//
// Refused (logged, webhook acknowledged) when the subscription can no
// longer take it: closed, cancelled at period end, or the payer is no
// longer the project's owner. Nothing was charged, so nothing to refund.
func (s *Service) applyPaymentMethodUpdate(ctx context.Context, payment *mollie.Payment) error {
	subscriptionID := payment.Metadata["subscription_id"]
	projectID := payment.Metadata["project_id"]
	if subscriptionID == "" || projectID == "" {
		slog.Error("billing.webhook.payment_method_update_missing_metadata", "mollie_payment_id", payment.ID)
		return nil
	}
	if payment.MandateID == "" {
		return fmt.Errorf("payment method update has no mandateId (payment %s)", payment.ID)
	}

	var status string
	var canceledAt *time.Time
	var mollieSub, subCust, ownerCust *string
	err := s.pool.QueryRow(ctx,
		`SELECT s.status, s.canceled_at, s.mollie_subscription_id, s.mollie_customer_id, u.mollie_customer_id
		   FROM public.subscriptions s
		   JOIN public.projects p ON p.id = s.project_id
		   JOIN public.platform_users u ON u.id = p.owner_id
		  WHERE s.id = $1 AND s.project_id = $2`,
		subscriptionID, projectID,
	).Scan(&status, &canceledAt, &mollieSub, &subCust, &ownerCust)
	if errors.Is(err, pgx.ErrNoRows) {
		slog.Warn("billing.webhook.payment_method_update_unknown_subscription",
			"subscription_id", subscriptionID, "mollie_payment_id", payment.ID)
		return nil
	}
	if err != nil {
		return fmt.Errorf("load subscription %s: %w", subscriptionID, err)
	}
	payerIsOwner := subCust != nil && ownerCust != nil && *subCust == payment.CustomerID && *ownerCust == payment.CustomerID
	if status != "active" || canceledAt != nil || mollieSub == nil || *mollieSub == "" || !payerIsOwner {
		slog.Warn("billing.webhook.payment_method_update_refused",
			"subscription_id", subscriptionID, "status", status,
			"canceled", canceledAt != nil, "payer_is_owner", payerIsOwner,
			"mollie_payment_id", payment.ID)
		return nil
	}

	newMandate, err := s.client.GetMandate(ctx, payment.CustomerID, payment.MandateID)
	if err != nil {
		return fmt.Errorf("fetch new mandate: %w", err)
	}
	if newMandate.Status == "invalid" {
		slog.Warn("billing.webhook.payment_method_update_invalid_mandate",
			"subscription_id", subscriptionID, "mandate_id", payment.MandateID)
		return nil
	}
	current, err := s.client.GetSubscription(ctx, payment.CustomerID, *mollieSub)
	if err != nil {
		return fmt.Errorf("fetch subscription: %w", err)
	}
	if current.MandateID == payment.MandateID {
		return nil // already applied (webhook redelivery)
	}
	// Two updates completed close together: keep the newer mandate even
	// if the older payment's webhook arrives last.
	if current.MandateID != "" {
		if cur, cerr := s.client.GetMandate(ctx, payment.CustomerID, current.MandateID); cerr == nil &&
			cur.Status != "invalid" && cur.CreatedAt.After(newMandate.CreatedAt) {
			slog.Info("billing.webhook.payment_method_update_superseded",
				"subscription_id", subscriptionID, "mandate_id", payment.MandateID)
			return nil
		}
	}
	if _, err := s.client.UpdateSubscription(ctx, payment.CustomerID, *mollieSub,
		mollie.SubscriptionUpdateRequest{MandateID: payment.MandateID}); err != nil {
		if errors.Is(err, mollie.ErrInvalidRequest) || errors.Is(err, mollie.ErrNotFound) {
			// e.g. the subscription was cancelled at Mollie meanwhile;
			// retrying won't help.
			slog.Error("billing.webhook.payment_method_update_rejected",
				"subscription_id", subscriptionID, "error", err)
			return nil
		}
		return fmt.Errorf("update subscription mandate: %w", err)
	}
	slog.Info("billing.webhook.payment_method_updated",
		"subscription_id", subscriptionID, "project_id", projectID,
		"method", newMandate.Method, "mandate_id", payment.MandateID)
	return nil
}

// HandleGetPaymentMethod is GET /platform/billing/projects/{project_id}/payment-method.
func HandleGetPaymentMethod(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := auth.ClaimsFromContext(r.Context())
		if !ok || claims == nil {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		projectID := chi.URLParam(r, "project_id")
		v, err := svc.GetPaymentMethod(r.Context(), claims.Subject, projectID)
		if err != nil {
			writePaymentMethodError(w, err, claims.Subject, projectID)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
}

// HandleStartPaymentMethodUpdate is POST /platform/billing/projects/{project_id}/payment-method.
// Returns {"checkout_url": "..."}.
func HandleStartPaymentMethodUpdate(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := auth.ClaimsFromContext(r.Context())
		if !ok || claims == nil {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		projectID := chi.URLParam(r, "project_id")
		url, err := svc.StartPaymentMethodUpdate(r.Context(), claims.Subject, projectID)
		if err != nil {
			writePaymentMethodError(w, err, claims.Subject, projectID)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"checkout_url": url})
	}
}

func writePaymentMethodError(w http.ResponseWriter, err error, userID, projectID string) {
	switch {
	case errors.Is(err, ErrBillingDisabled):
		writeJSONError(w, http.StatusServiceUnavailable, "billing_disabled", "billing is not enabled in this environment")
	case errors.Is(err, ErrSubscriptionNotFound):
		writeJSONError(w, http.StatusNotFound, "subscription_not_found", "no live subscription for this project")
	case errors.Is(err, ErrNotPayer):
		writeJSONError(w, http.StatusConflict, "not_payer", "this project's subscription is paid by its previous owner")
	case errors.Is(err, ErrPaymentOverdue):
		writeJSONError(w, http.StatusConflict, "payment_overdue", "the last payment failed; use Pay now to settle it with a new payment method")
	case errors.Is(err, ErrPaymentMethodNotChangeable):
		writeJSONError(w, http.StatusConflict, "not_changeable", "the payment method can't be changed for a subscription that is cancelled or not set up yet")
	case errors.Is(err, mollie.ErrServer) || errors.Is(err, mollie.ErrRateLimited) || errors.Is(err, mollie.ErrNotFound):
		slog.Error("billing: payment method request failed at Mollie", "user_id", userID, "project_id", projectID, "error", err)
		writeJSONError(w, http.StatusBadGateway, "payment_provider_error", "couldn't reach the payment provider, try again")
	default:
		slog.Error("billing: payment method request failed", "user_id", userID, "project_id", projectID, "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "payment method request failed")
	}
}
