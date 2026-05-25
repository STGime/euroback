package billing

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// Tests for the Mollie HTTP client and webhook signature verification.
// All Mollie endpoints are mocked via httptest; no real network calls
// land here. Closes the unit-test portion of issue #157.

func newTestClient(t *testing.T, handler http.HandlerFunc) (*MollieClient, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	c := NewMollieClient("test_dummy", "test_webhook_secret")
	c.baseURL = srv.URL
	return c, srv
}

func TestMollieClient_Configured(t *testing.T) {
	cases := []struct {
		apiKey, secret string
		want           bool
	}{
		{"", "", false},
		{"test_x", "", false},
		{"", "wh", false},
		{"test_x", "wh", true},
	}
	for _, c := range cases {
		got := NewMollieClient(c.apiKey, c.secret).Configured()
		if got != c.want {
			t.Errorf("Configured(apiKey=%q, secret=%q) = %v, want %v",
				c.apiKey, c.secret, got, c.want)
		}
	}
}

func TestMollieClient_TestMode(t *testing.T) {
	if !NewMollieClient("test_abc", "wh").TestMode() {
		t.Error("test_ prefix should be TestMode")
	}
	if NewMollieClient("live_abc", "wh").TestMode() {
		t.Error("live_ prefix should NOT be TestMode")
	}
}

// TestMollieClient_CreateCustomer asserts POST /customers shape and
// the returned ID parsing.
func TestMollieClient_CreateCustomer(t *testing.T) {
	var captured struct {
		method, path, auth string
		body               map[string]string
	}
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		captured.method = r.Method
		captured.path = r.URL.Path
		captured.auth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &captured.body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"cst_abc","email":"a@b.c","name":"A B"}`))
	})
	defer srv.Close()

	id, err := c.CreateCustomer(context.Background(), "a@b.c", "A B")
	if err != nil {
		t.Fatalf("CreateCustomer: %v", err)
	}
	if id != "cst_abc" {
		t.Errorf("id: got %q want cst_abc", id)
	}
	if captured.method != "POST" || captured.path != "/customers" {
		t.Errorf("request: %s %s, want POST /customers", captured.method, captured.path)
	}
	if captured.auth != "Bearer test_dummy" {
		t.Errorf("auth header: %q", captured.auth)
	}
	if captured.body["email"] != "a@b.c" || captured.body["name"] != "A B" {
		t.Errorf("body: %v", captured.body)
	}
}

// TestMollieClient_CreateFirstPayment verifies the sequenceType=first
// shape that captures a mandate, and that the returned checkout URL
// surfaces.
func TestMollieClient_CreateFirstPayment(t *testing.T) {
	var captured map[string]any
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"tr_xyz",
			"status":"open",
			"amount":{"currency":"EUR","value":"9.00"},
			"_links":{"checkout":{"href":"https://www.mollie.com/checkout/select-method/xyz"}}
		}`))
	})
	defer srv.Close()

	p, err := c.CreateFirstPayment(context.Background(), FirstPaymentRequest{
		AmountEUR:   "9.00",
		CustomerID:  "cst_abc",
		RedirectURL: "https://app/return",
		WebhookURL:  "https://api/webhooks/mollie",
		Description: "Pro",
		Metadata:    map[string]string{"project_id": "p1"},
	})
	if err != nil {
		t.Fatalf("CreateFirstPayment: %v", err)
	}
	if p.ID != "tr_xyz" {
		t.Errorf("payment id: got %q want tr_xyz", p.ID)
	}
	if p.Links.Checkout.Href == "" {
		t.Error("checkout href missing")
	}
	if captured["sequenceType"] != "first" {
		t.Errorf("sequenceType: want 'first' to capture the mandate, got %v", captured["sequenceType"])
	}
	if captured["customerId"] != "cst_abc" {
		t.Errorf("customerId: %v", captured["customerId"])
	}
	amt, _ := captured["amount"].(map[string]any)
	if amt["currency"] != "EUR" || amt["value"] != "9.00" {
		t.Errorf("amount: %v", amt)
	}
}

// TestMollieClient_GetPayment fetches the authoritative state.
func TestMollieClient_GetPayment(t *testing.T) {
	var hits int32
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if r.Method != "GET" || r.URL.Path != "/payments/tr_x" {
			t.Errorf("unexpected: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"tr_x",
			"status":"paid",
			"amount":{"currency":"EUR","value":"9.00"},
			"customerId":"cst_x",
			"metadata":{"project_id":"p1","plan":"pro"}
		}`))
	})
	defer srv.Close()

	p, err := c.GetPayment(context.Background(), "tr_x")
	if err != nil {
		t.Fatalf("GetPayment: %v", err)
	}
	if p.Status != "paid" {
		t.Errorf("status: %q", p.Status)
	}
	if p.Metadata["project_id"] != "p1" {
		t.Errorf("metadata: %v", p.Metadata)
	}
	if hits != 1 {
		t.Errorf("expected 1 hit, got %d", hits)
	}
}

func TestMollieClient_ErrorSurfacesDetail(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(422)
		_, _ = w.Write([]byte(`{"status":422,"title":"Unprocessable Entity","detail":"amount.value is invalid"}`))
	})
	defer srv.Close()
	_, err := c.CreateCustomer(context.Background(), "x", "y")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "422") || !strings.Contains(err.Error(), "amount.value is invalid") {
		t.Errorf("error should surface Mollie detail: %v", err)
	}
}

// TestMollieClient_VerifyWebhookSignature exercises both the happy and
// adversarial paths. The signature is HMAC-SHA256(body, secret) hex.
func TestMollieClient_VerifyWebhookSignature(t *testing.T) {
	c := NewMollieClient("test_x", "topsecret")
	body := []byte("id=tr_abc")
	// Hex of HMAC-SHA256("id=tr_abc", "topsecret"). Computed once
	// and pinned as a fixture; recompute with:
	//   echo -n "id=tr_abc" | openssl dgst -sha256 -hmac "topsecret"
	good := "5d45ed6bc4c6d9e6b62f30bb3bc6fada7f104ae7261dec794792faf26812ba2d"

	if err := c.VerifyWebhookSignature(body, good); err != nil {
		t.Errorf("expected good signature to verify, got %v", err)
	}
	if err := c.VerifyWebhookSignature(body, "00"+good[2:]); err == nil {
		t.Error("expected mis-signed body to fail")
	}
	if err := c.VerifyWebhookSignature(body, ""); err == nil {
		t.Error("expected empty signature to fail")
	}
	// Bad-secret path: configure no secret → method refuses to even try.
	noSecret := NewMollieClient("test_x", "")
	if err := noSecret.VerifyWebhookSignature(body, good); err == nil {
		t.Error("expected unconfigured-secret to fail")
	}
}

// TestMollieClient_CancelSubscription verifies the DELETE path shape.
func TestMollieClient_CancelSubscription(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "DELETE" {
			t.Errorf("method: %s want DELETE", r.Method)
		}
		if r.URL.Path != "/customers/cst_x/subscriptions/sub_y" {
			t.Errorf("path: %s", r.URL.Path)
		}
		w.WriteHeader(200)
	})
	defer srv.Close()
	if err := c.CancelSubscription(context.Background(), "cst_x", "sub_y"); err != nil {
		t.Errorf("CancelSubscription: %v", err)
	}
}

func TestEuroStringToCents(t *testing.T) {
	cases := map[string]int{
		"9.00":  900,
		"9":     900,
		"9.5":   950,
		"9.99":  999,
		"0.01":  1,
		"0":     0,
		"":      0,
		"abc":   0,
		"9.999": 999, // truncates at 2 decimals (Mollie never returns more)
	}
	for in, want := range cases {
		if got := euroStringToCents(in); got != want {
			t.Errorf("euroStringToCents(%q) = %d, want %d", in, got, want)
		}
	}
}
