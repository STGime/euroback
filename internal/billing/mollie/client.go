package mollie

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Env selects test or live mode. It's a compile-time string enum
// rather than a bool because "live mode with test key" and vice-versa
// are silent bugs on Mollie's side (the API accepts the wrong-mode
// key and returns 401 with a generic "unauthorized" title). Making
// the env explicit at construction time forces the operator to think
// about which environment they're wiring.
type Env string

const (
	EnvTest Env = "test"
	EnvLive Env = "live"
)

// Config carries the settings NewClient needs. APIKey must match the
// Env — test_ prefix for EnvTest, live_ prefix for EnvLive. The
// client does not validate the prefix (Mollie may change the format
// in future); it does log a warning at construction time if the
// prefix is suspicious.
type Config struct {
	// APIKey is the bearer token from the Mollie dashboard. Empty
	// value is legal — the client will refuse all requests and log
	// them, matching the pattern the email package uses when
	// running against a dev environment without TEM credentials.
	APIKey string

	// Env is EnvTest or EnvLive. Anything else is treated as EnvTest
	// so a typo in the k8s ConfigMap can't push a test-key call
	// into live-mode routing.
	Env Env

	// BaseURL is the Mollie API root. Left empty in production; set
	// in tests to point at an httptest server. Trailing slash is
	// removed at construction time.
	BaseURL string

	// HTTPClient lets tests inject a client with a fake transport.
	// Nil uses a client with a 15-second timeout (Mollie's own SLA
	// promises sub-second p99 for the endpoints we hit, so 15s is
	// generous cover for retries + TLS handshake in cold-start
	// scenarios).
	HTTPClient *http.Client
}

// Client is a thin REST wrapper around Mollie. Zero methods do
// business logic — everything is a straight HTTP call with JSON
// (de)serialisation. State-machine work lives one level up in
// internal/billing.
type Client struct {
	apiKey     string
	env        Env
	baseURL    string
	httpClient *http.Client
}

// defaultBaseURL is Mollie's REST root. There's only one — Mollie
// switches between test and live mode based on the API key, not on a
// URL. Callers set Config.BaseURL only in tests.
const defaultBaseURL = "https://api.mollie.com/v2"

// NewClient constructs a client from Config. It's cheap — no network
// calls, no goroutines. Safe to call at server startup even when
// billing is not yet enabled; unconfigured clients return typed
// errors on any endpoint call rather than panic.
func NewClient(cfg Config) *Client {
	c := &Client{
		apiKey:  strings.TrimSpace(cfg.APIKey),
		env:     cfg.Env,
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
	}
	if c.baseURL == "" {
		c.baseURL = defaultBaseURL
	}
	if cfg.Env != EnvTest && cfg.Env != EnvLive {
		c.env = EnvTest
	}
	c.httpClient = cfg.HTTPClient
	if c.httpClient == nil {
		c.httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	// Log a warning at construction time on prefix mismatch so an
	// ops mistake is visible in the first minute after deploy
	// rather than the first failed checkout.
	if c.apiKey != "" {
		switch c.env {
		case EnvTest:
			if !strings.HasPrefix(c.apiKey, "test_") {
				slog.Warn("mollie: test env but API key does not start with test_", "prefix", firstN(c.apiKey, 5))
			}
		case EnvLive:
			if !strings.HasPrefix(c.apiKey, "live_") {
				slog.Warn("mollie: live env but API key does not start with live_", "prefix", firstN(c.apiKey, 5))
			}
		}
	}
	return c
}

// Configured returns true if the client has an API key. Empty-key
// clients are legal at construction time (so the process can boot
// without secrets in a dev environment) but every endpoint returns
// ErrUnauthorized immediately without hitting the network.
func (c *Client) Configured() bool { return c.apiKey != "" }

// Env returns the client's selected environment. Used by the billing
// service to pick a webhook URL (test-mode webhooks go to a staging
// URL; live-mode webhooks go to prod).
func (c *Client) Env() Env { return c.env }

// CreateCustomer POSTs /v2/customers and returns the Mollie-assigned
// customer object. Metadata should include at least {"platform_user_id":
// "..."} so operators can trace back from the Mollie dashboard.
func (c *Client) CreateCustomer(ctx context.Context, req CustomerCreateRequest) (*Customer, error) {
	var out Customer
	if err := c.do(ctx, http.MethodPost, "/customers", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetCustomer fetches an existing customer. Returns ErrNotFound if
// Mollie 404s (e.g. customer manually deleted from dashboard).
func (c *Client) GetCustomer(ctx context.Context, id string) (*Customer, error) {
	var out Customer
	if err := c.do(ctx, http.MethodGet, "/customers/"+id, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateSubscription POSTs /v2/customers/{id}/subscriptions. Because
// Mollie requires a mandate before recurring subscriptions can charge
// silently, this endpoint should only be called after the initial
// first-payment flow has captured a mandate. The billing service
// handles that ordering — this method is a straight passthrough.
func (c *Client) CreateSubscription(ctx context.Context, customerID string, req SubscriptionCreateRequest) (*Subscription, error) {
	var out Subscription
	if err := c.do(ctx, http.MethodPost, "/customers/"+customerID+"/subscriptions", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetSubscription fetches an existing subscription. Used by the
// webhook handler when Mollie pings us about a subscription state
// change (Mollie's webhook body is just {id}; canonical state comes
// from a GET-back to the API).
func (c *Client) GetSubscription(ctx context.Context, customerID, subscriptionID string) (*Subscription, error) {
	var out Subscription
	if err := c.do(ctx, http.MethodGet, "/customers/"+customerID+"/subscriptions/"+subscriptionID, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CancelSubscription flips a subscription to canceled. Mollie
// enforces end-of-period semantics by default (no partial refund
// unless a refund is separately created). Returns the canceled
// resource so callers can mirror status + canceledAt into our DB.
func (c *Client) CancelSubscription(ctx context.Context, customerID, subscriptionID string) (*Subscription, error) {
	var out Subscription
	if err := c.do(ctx, http.MethodDelete, "/customers/"+customerID+"/subscriptions/"+subscriptionID, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetPayment fetches a payment by ID. Used by the webhook handler on
// every /platform/billing/webhook POST — Mollie sends only {id}, the
// canonical state comes from a GET-back.
func (c *Client) GetPayment(ctx context.Context, id string) (*Payment, error) {
	var out Payment
	if err := c.do(ctx, http.MethodGet, "/payments/"+id, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateRefund refunds a payment. Amount can be less than the payment
// total for partial refunds (Eurobase uses this for prorated
// cancellations). Returns the created refund object.
func (c *Client) CreateRefund(ctx context.Context, paymentID string, req RefundCreateRequest) (*Refund, error) {
	var out Refund
	if err := c.do(ctx, http.MethodPost, "/payments/"+paymentID+"/refunds", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// do is the shared request-execution path. It handles auth header,
// JSON marshalling, response classification, and body draining. All
// public methods are ~3 lines of glue on top of this.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	if !c.Configured() {
		return ErrUnauthorized
	}

	var reqBody io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("mollie: marshal request: %w", err)
		}
		reqBody = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reqBody)
	if err != nil {
		return fmt.Errorf("mollie: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("mollie: http: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("mollie: read response: %w", err)
	}

	if resp.StatusCode >= 300 {
		// Try to decode Mollie's structured error envelope; if it
		// isn't valid JSON, fall through to the raw body — classify
		// still returns a sensible sentinel.
		var envelope APIError
		_ = json.Unmarshal(respBody, &envelope)
		envelope.Status = resp.StatusCode
		envelope.Body = string(respBody)
		slog.Warn("mollie: non-2xx response",
			"method", method,
			"path", path,
			"status", resp.StatusCode,
			"title", envelope.Title,
			"detail", envelope.Detail,
		)
		return classify(&envelope)
	}

	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("mollie: decode response: %w", err)
		}
	}
	return nil
}

// firstN returns the first n bytes of s, safely truncated. Used for
// slog fields where we want a hint at the API-key prefix without
// leaking the whole token.
func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
