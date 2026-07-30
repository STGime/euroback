package mollie

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeServer is a tiny httptest wrapper that lets tests script per-
// path handlers. It captures the last request body so assertions can
// verify what the client sent to Mollie without stubbing json.Marshal.
type fakeServer struct {
	t       *testing.T
	server  *httptest.Server
	handler http.HandlerFunc
	lastReq struct {
		Method string
		Path   string
		Auth   string
		Body   []byte
	}
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	f := &fakeServer{t: t}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.lastReq.Method = r.Method
		f.lastReq.Path = r.URL.Path
		f.lastReq.Auth = r.Header.Get("Authorization")
		f.lastReq.Body = body
		if f.handler == nil {
			http.Error(w, "no handler", http.StatusInternalServerError)
			return
		}
		// Reset the body so the handler can re-read if it wants.
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		f.handler(w, r)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeServer) client() *Client {
	return NewClient(Config{
		APIKey:  "test_dummy_key",
		Env:     EnvTest,
		BaseURL: f.server.URL,
	})
}

func writeJSON(t *testing.T, w http.ResponseWriter, status int, body any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Fatalf("write json: %v", err)
	}
}

// ── Auth + configuration ────────────────────────────────────────────

func TestClient_UnconfiguredReturnsUnauthorized(t *testing.T) {
	c := NewClient(Config{APIKey: "", Env: EnvTest})
	_, err := c.GetCustomer(context.Background(), "cst_dummy")
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("want ErrUnauthorized on empty API key, got %v", err)
	}
	if c.Configured() {
		t.Fatal("Configured() should be false for empty API key")
	}
}

func TestClient_ForwardsBearerToken(t *testing.T) {
	f := newFakeServer(t)
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, Customer{ID: "cst_x", Mode: "test"})
	}
	_, err := f.client().GetCustomer(context.Background(), "cst_x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "Bearer test_dummy_key"; f.lastReq.Auth != want {
		t.Errorf("Authorization = %q, want %q", f.lastReq.Auth, want)
	}
}

func TestClient_EnvDefaultsToTestOnGarbage(t *testing.T) {
	c := NewClient(Config{APIKey: "test_x", Env: Env("nonsense")})
	if c.Env() != EnvTest {
		t.Errorf("garbage env should fall back to test, got %q", c.Env())
	}
}

// ── Customers ───────────────────────────────────────────────────────

func TestClient_CreateCustomerRoundtrip(t *testing.T) {
	f := newFakeServer(t)
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/customers" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		writeJSON(t, w, http.StatusCreated, Customer{
			ID:        "cst_abc",
			Mode:      "test",
			Email:     "stefan@example.com",
			CreatedAt: time.Now().UTC(),
			Metadata:  map[string]string{"platform_user_id": "usr_1"},
		})
	}
	cust, err := f.client().CreateCustomer(context.Background(), CustomerCreateRequest{
		Email:    "stefan@example.com",
		Metadata: map[string]string{"platform_user_id": "usr_1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cust.ID != "cst_abc" {
		t.Errorf("customer ID = %q, want cst_abc", cust.ID)
	}
	// Verify the request body carried our fields — catches marshal
	// regressions before they hit Mollie in staging.
	var sent CustomerCreateRequest
	if err := json.Unmarshal(f.lastReq.Body, &sent); err != nil {
		t.Fatalf("decode sent body: %v", err)
	}
	if sent.Email != "stefan@example.com" {
		t.Errorf("sent email = %q", sent.Email)
	}
	if sent.Metadata["platform_user_id"] != "usr_1" {
		t.Errorf("sent metadata = %v", sent.Metadata)
	}
}

// ── Subscriptions ───────────────────────────────────────────────────

func TestClient_CreateSubscription(t *testing.T) {
	f := newFakeServer(t)
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/customers/cst_abc/subscriptions" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		writeJSON(t, w, http.StatusCreated, Subscription{
			ID:          "sub_xyz",
			CustomerID:  "cst_abc",
			Status:      "pending",
			Amount:      AmountFromCents(1900, "EUR"),
			Interval:    "1 month",
			Description: "Eurobase Pro",
			CreatedAt:   time.Now().UTC(),
		})
	}
	sub, err := f.client().CreateSubscription(context.Background(), "cst_abc", SubscriptionCreateRequest{
		Amount:      AmountFromCents(1900, "EUR"),
		Interval:    "1 month",
		Description: "Eurobase Pro",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sub.ID != "sub_xyz" || sub.Status != "pending" {
		t.Errorf("subscription = %+v", sub)
	}
}

func TestClient_CancelSubscription(t *testing.T) {
	f := newFakeServer(t)
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("want DELETE, got %s", r.Method)
		}
		now := time.Now().UTC()
		writeJSON(t, w, http.StatusOK, Subscription{
			ID:         "sub_xyz",
			CustomerID: "cst_abc",
			Status:     "canceled",
			CanceledAt: &now,
		})
	}
	sub, err := f.client().CancelSubscription(context.Background(), "cst_abc", "sub_xyz")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sub.Status != "canceled" || sub.CanceledAt == nil {
		t.Errorf("cancel response = %+v", sub)
	}
}

// ── Payments + Refunds ──────────────────────────────────────────────

func TestClient_GetPayment(t *testing.T) {
	f := newFakeServer(t)
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, Payment{
			ID:             "tr_1",
			Status:         "paid",
			Amount:         AmountFromCents(1900, "EUR"),
			SubscriptionID: "sub_xyz",
			SequenceType:   SequenceTypeRecurring,
		})
	}
	p, err := f.client().GetPayment(context.Background(), "tr_1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Status != "paid" || p.Amount.Cents() != 1900 {
		t.Errorf("payment = %+v", p)
	}
}

func TestClient_CreateRefund(t *testing.T) {
	f := newFakeServer(t)
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/refunds") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		writeJSON(t, w, http.StatusCreated, Refund{
			ID:        "re_1",
			PaymentID: "tr_1",
			Amount:    AmountFromCents(500, "EUR"),
			Status:    "pending",
		})
	}
	r, err := f.client().CreateRefund(context.Background(), "tr_1", RefundCreateRequest{
		Amount:      AmountFromCents(500, "EUR"),
		Description: "prorated cancel",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.ID != "re_1" {
		t.Errorf("refund ID = %q", r.ID)
	}
}

// ── Error classification ────────────────────────────────────────────

func TestClient_404IsNotFound(t *testing.T) {
	f := newFakeServer(t)
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusNotFound, APIError{Status: 404, Title: "Not Found", Detail: "no such customer"})
	}
	_, err := f.client().GetCustomer(context.Background(), "cst_missing")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
	var api *APIError
	if !errors.As(err, &api) {
		t.Errorf("expected *APIError in chain, got %T", err)
	}
	if api != nil && api.Title != "Not Found" {
		t.Errorf("api.Title = %q", api.Title)
	}
}

func TestClient_401IsUnauthorized(t *testing.T) {
	f := newFakeServer(t)
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusUnauthorized, APIError{Status: 401, Title: "Unauthorized"})
	}
	_, err := f.client().GetCustomer(context.Background(), "cst_x")
	if !errors.Is(err, ErrUnauthorized) {
		t.Errorf("want ErrUnauthorized, got %v", err)
	}
}

func TestClient_429IsRateLimited(t *testing.T) {
	f := newFakeServer(t)
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusTooManyRequests, APIError{Status: 429, Title: "Too Many Requests"})
	}
	_, err := f.client().GetCustomer(context.Background(), "cst_x")
	if !errors.Is(err, ErrRateLimited) {
		t.Errorf("want ErrRateLimited, got %v", err)
	}
}

func TestClient_500IsServerError(t *testing.T) {
	f := newFakeServer(t)
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}
	_, err := f.client().GetCustomer(context.Background(), "cst_x")
	if !errors.Is(err, ErrServer) {
		t.Errorf("want ErrServer, got %v", err)
	}
}

func TestClient_InvalidJSONResponse(t *testing.T) {
	f := newFakeServer(t)
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{not valid json"))
	}
	_, err := f.client().GetCustomer(context.Background(), "cst_x")
	if err == nil {
		t.Fatal("want decode error on invalid JSON, got nil")
	}
}

// ── Amount helpers ──────────────────────────────────────────────────

func TestAmountFromCents(t *testing.T) {
	tests := []struct {
		cents int
		want  string
	}{
		{0, "0.00"},
		{1, "0.01"},
		{9, "0.09"},
		{99, "0.99"},
		{100, "1.00"},
		{1900, "19.00"},
		{14900, "149.00"},
		{-5, "0.00"}, // negative cents guarded to zero
	}
	for _, tt := range tests {
		got := AmountFromCents(tt.cents, "EUR")
		if got.Value != tt.want {
			t.Errorf("AmountFromCents(%d) = %q, want %q", tt.cents, got.Value, tt.want)
		}
		if got.Currency != "EUR" {
			t.Errorf("currency = %q", got.Currency)
		}
	}
}

func TestAmountFromCents_DefaultsToEUR(t *testing.T) {
	got := AmountFromCents(100, "")
	if got.Currency != "EUR" {
		t.Errorf("empty currency should default to EUR, got %q", got.Currency)
	}
}

func TestAmount_Cents(t *testing.T) {
	tests := []struct {
		value string
		want  int
	}{
		{"0.00", 0},
		{"0.01", 1},
		{"0.99", 99},
		{"1.00", 100},
		{"19.00", 1900},
		{"149.00", 14900},
		{"19.5", 1950},   // one-decimal input
		{"garbage", 0},   // parse failure returns 0
		{"", 0},
	}
	for _, tt := range tests {
		got := Amount{Currency: "EUR", Value: tt.value}.Cents()
		if got != tt.want {
			t.Errorf("Amount{%q}.Cents() = %d, want %d", tt.value, got, tt.want)
		}
	}
}

// ── Roundtrip: cents → string → cents ──────────────────────────────

func TestAmountRoundtrip(t *testing.T) {
	for _, cents := range []int{0, 1, 100, 1900, 14900, 999999} {
		got := AmountFromCents(cents, "EUR").Cents()
		if got != cents {
			t.Errorf("roundtrip %d → %d", cents, got)
		}
	}
}
