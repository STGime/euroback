// Package billing integrates Mollie (Netherlands, EU) for subscription
// payments. Closes #2.
//
// Mollie is the payment processor because the project's CLAUDE.md
// explicitly forbids US-jurisdiction services and Mollie is already
// listed in the DPA sub-processor registry (migration 000025). The
// integration uses Mollie REST v2 — a "first payment" captures a
// mandate, and subsequent monthly subscriptions charge against that
// mandate automatically.
//
// Everything here speaks plain HTTPS against Mollie. There is no
// official Go SDK we trust; the API surface we need is small enough
// (5 calls) that a typed wrapper keeps the dependency footprint zero.
package billing

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// MollieClient is a typed wrapper around the Mollie REST API.
type MollieClient struct {
	httpClient    *http.Client
	apiKey        string
	webhookSecret string
	baseURL       string // injectable for tests; defaults to https://api.mollie.com/v2
}

// NewMollieClient creates a Mollie client. Pass an empty apiKey only
// in tests; runtime callers MUST provide one — Configured() reports
// the state for the startup gate.
func NewMollieClient(apiKey, webhookSecret string) *MollieClient {
	return &MollieClient{
		httpClient:    &http.Client{Timeout: 15 * time.Second},
		apiKey:        apiKey,
		webhookSecret: webhookSecret,
		baseURL:       "https://api.mollie.com/v2",
	}
}

// Configured reports whether the client has the credentials it needs
// to talk to Mollie. The gateway startup gate calls this before
// mounting the /billing routes so a missing key fails closed rather
// than silently degrading checkout to a no-op.
func (c *MollieClient) Configured() bool {
	return c.apiKey != "" && c.webhookSecret != ""
}

// TestMode reports whether the configured API key is a Mollie test
// key (test_*). Used by the runbook + startup banner so it's
// obvious when a non-prod deploy is talking to the sandbox.
func (c *MollieClient) TestMode() bool {
	return strings.HasPrefix(c.apiKey, "test_")
}

// ── Customer ────────────────────────────────────────────────────────────────

// MollieCustomer is the subset of the Mollie Customer object we read
// back. The full object has more fields (locale, metadata, etc.); we
// only need id + email to round-trip identity.
type MollieCustomer struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

// CreateCustomer creates a Mollie customer for the given email + name.
// Returns the Mollie customer ID (string starting with "cst_...").
// The caller is responsible for storing that ID against
// platform_users.mollie_customer_id; the service layer wraps this
// with idempotency by checking the DB first.
func (c *MollieClient) CreateCustomer(ctx context.Context, email, name string) (string, error) {
	body := map[string]string{"email": email, "name": name}
	var out MollieCustomer
	if err := c.do(ctx, http.MethodPost, "/customers", body, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

// ── First payment (captures mandate for recurring) ──────────────────────────

// MolliePayment mirrors the fields of a Mollie Payment object that we
// read when reconciling webhook events. We deliberately do NOT model
// the entire object — Mollie has dozens of fields, most of which are
// irrelevant to our state machine.
type MolliePayment struct {
	ID         string `json:"id"`
	Status     string `json:"status"` // open | paid | failed | canceled | expired
	Amount     struct {
		Currency string `json:"currency"`
		Value    string `json:"value"` // decimal string e.g. "9.00"
	} `json:"amount"`
	CustomerID    string             `json:"customerId"`
	SubscriptionID string            `json:"subscriptionId,omitempty"`
	MandateID     string             `json:"mandateId,omitempty"`
	PaidAt        *time.Time         `json:"paidAt,omitempty"`
	Description   string             `json:"description"`
	Metadata      map[string]string  `json:"metadata,omitempty"`
	Links         MolliePaymentLinks `json:"_links"`
}

// MolliePaymentLinks is the HATEOAS block Mollie returns. The
// checkout URL is the one we redirect the browser to.
type MolliePaymentLinks struct {
	Checkout struct {
		Href string `json:"href"`
	} `json:"checkout"`
}

// FirstPaymentRequest is the request body for the "first payment"
// pattern that creates a mandate. The mandate is the artifact that
// lets us charge the customer later (via subscription) without
// re-prompting for card details.
type FirstPaymentRequest struct {
	AmountEUR  string            // e.g. "9.00"
	CustomerID string            // cst_...
	RedirectURL string           // user's browser lands here after Mollie checkout
	WebhookURL string            // Mollie POSTs here on payment status changes
	Description string           // human-readable, shows on the Mollie receipt
	Metadata   map[string]string // we put projectID + pendingProjectID here
}

// CreateFirstPayment captures a mandate for recurring charges and
// returns the checkout URL the browser must redirect to. The mandate
// becomes valid once Mollie webhooks back payment.status=paid; the
// real subscription is then created in CreateSubscription against
// that mandate.
func (c *MollieClient) CreateFirstPayment(ctx context.Context, req FirstPaymentRequest) (*MolliePayment, error) {
	body := map[string]any{
		"amount":       map[string]string{"currency": "EUR", "value": req.AmountEUR},
		"customerId":   req.CustomerID,
		"sequenceType": "first", // captures mandate
		"description":  req.Description,
		"redirectUrl":  req.RedirectURL,
		"webhookUrl":   req.WebhookURL,
		"metadata":     req.Metadata,
	}
	var out MolliePayment
	if err := c.do(ctx, http.MethodPost, "/payments", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetPayment fetches the current state of a payment by ID. The
// webhook handler uses this to verify a payment ID handed to us by
// the (untrusted) webhook against Mollie's authoritative state.
func (c *MollieClient) GetPayment(ctx context.Context, paymentID string) (*MolliePayment, error) {
	var out MolliePayment
	if err := c.do(ctx, http.MethodGet, "/payments/"+paymentID, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ── Subscriptions ───────────────────────────────────────────────────────────

// MollieSubscription is what Mollie returns after creating a recurring
// subscription against a captured mandate.
type MollieSubscription struct {
	ID       string `json:"id"`
	Status   string `json:"status"` // active | pending | canceled | suspended | completed
	Amount   struct {
		Currency string `json:"currency"`
		Value    string `json:"value"`
	} `json:"amount"`
	Interval    string             `json:"interval"`    // "1 month"
	Description string             `json:"description"`
	NextPaymentDate *string        `json:"nextPaymentDate,omitempty"`
	WebhookURL  string             `json:"webhookUrl"`
}

// CreateSubscription starts a recurring monthly subscription against
// an already-captured mandate. Call this from the webhook handler
// when the first payment lands as paid, NOT at checkout time.
func (c *MollieClient) CreateSubscription(ctx context.Context, customerID, amountEUR, description, webhookURL string) (*MollieSubscription, error) {
	body := map[string]any{
		"amount":      map[string]string{"currency": "EUR", "value": amountEUR},
		"interval":    "1 month",
		"description": description,
		"webhookUrl":  webhookURL,
	}
	var out MollieSubscription
	path := fmt.Sprintf("/customers/%s/subscriptions", customerID)
	if err := c.do(ctx, http.MethodPost, path, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CancelSubscription marks a Mollie subscription as cancelled. The
// effective behaviour is "no further charges"; the customer keeps
// access until current_period_end at the platform level. Mollie has
// no concept of "cancel at period end" itself — that's our state
// machine's responsibility.
func (c *MollieClient) CancelSubscription(ctx context.Context, customerID, subscriptionID string) error {
	path := fmt.Sprintf("/customers/%s/subscriptions/%s", customerID, subscriptionID)
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

// ── Webhook signature verification ──────────────────────────────────────────

// VerifyWebhookSignature returns nil if the signature header matches
// the body computed under the shared secret. Mollie signs webhooks
// with HMAC-SHA256 in the `X-Mollie-Signature` header. We never
// trust the webhook body alone — even after this passes we re-fetch
// the payment from Mollie's API to authoritatively confirm state.
func (c *MollieClient) VerifyWebhookSignature(body []byte, signatureHeader string) error {
	if c.webhookSecret == "" {
		return fmt.Errorf("webhook secret not configured")
	}
	mac := hmac.New(sha256.New, []byte(c.webhookSecret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	// constant-time compare to defeat timing oracles
	if !hmac.Equal([]byte(expected), []byte(signatureHeader)) {
		return fmt.Errorf("signature mismatch")
	}
	return nil
}

// ── Internal HTTP plumbing ──────────────────────────────────────────────────

// do issues a JSON HTTP request to Mollie. body may be nil for GET/DELETE;
// out may be nil to discard the response. All Mollie errors come back
// as JSON with a `status` (HTTP code echoed) + `title` + `detail`;
// we surface those verbatim in the wrapping error so the audit log
// captures what Mollie actually said.
func (c *MollieClient) do(ctx context.Context, method, path string, body any, out any) error {
	var reqBody io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		reqBody = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reqBody)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("mollie %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	rawResp, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		// Mollie shape: {"status":422,"title":"Unprocessable Entity","detail":"..."}
		var apiErr struct {
			Status int    `json:"status"`
			Title  string `json:"title"`
			Detail string `json:"detail"`
		}
		_ = json.Unmarshal(rawResp, &apiErr)
		slog.Warn("mollie api error",
			"method", method, "path", path,
			"status", resp.StatusCode, "title", apiErr.Title, "detail", apiErr.Detail,
		)
		return fmt.Errorf("mollie %s %s -> %d %s: %s", method, path, resp.StatusCode, apiErr.Title, apiErr.Detail)
	}
	if out != nil {
		if err := json.Unmarshal(rawResp, out); err != nil {
			return fmt.Errorf("decode mollie response: %w", err)
		}
	}
	return nil
}
