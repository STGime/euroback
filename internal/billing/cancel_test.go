package billing

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/billing/mollie"
)

func TestCancelHandler_DisabledReturns503(t *testing.T) {
	c := mollie.NewClient(mollie.Config{APIKey: "test_x", Env: mollie.EnvTest})
	svc := NewService(nil, c, Config{}, false)
	req := httptest.NewRequest(http.MethodPost, "/platform/billing/subscriptions/sub_x/cancel", strings.NewReader(`{"mode":"immediate"}`))
	req = req.WithContext(auth.ContextWithClaims(req.Context(), &auth.Claims{Subject: "usr_1"}))
	w := httptest.NewRecorder()
	HandleCancelSubscription(svc).ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("want 503, got %d", w.Code)
	}
}

func TestCancelHandler_UnauthenticatedReturns401(t *testing.T) {
	c := mollie.NewClient(mollie.Config{APIKey: "test_x", Env: mollie.EnvTest})
	svc := NewService(nil, c, Config{}, true)
	req := httptest.NewRequest(http.MethodPost, "/platform/billing/subscriptions/sub_x/cancel", strings.NewReader(`{"mode":"end_of_period"}`))
	// no claims
	w := httptest.NewRecorder()
	HandleCancelSubscription(svc).ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("want 401, got %d", w.Code)
	}
}

// TestCancelService_ModeValidation covers the branches that can
// run without a pool: unknown/empty modes and the disabled
// short-circuit. Real cancel flows exercise via manual staging QA
// (Mollie test mode + real Postgres).
func TestCancelService_ModeValidation(t *testing.T) {
	c := mollie.NewClient(mollie.Config{APIKey: "test_x", Env: mollie.EnvTest})

	// enabled=false → ErrBillingDisabled regardless of mode.
	svc := NewService(nil, c, Config{}, false)
	if _, err := svc.CancelSubscription(context.Background(), "usr_1", "sub_1", CancelModeImmediate); !errors.Is(err, ErrBillingDisabled) {
		t.Errorf("disabled: want ErrBillingDisabled, got %v", err)
	}

	// Note: the invalid-mode branch runs BEFORE the pool touch,
	// so we can hit it safely with nil pool + enabled=true.
	svc2 := NewService(nil, c, Config{}, true)
	if _, err := svc2.CancelSubscription(context.Background(), "usr_1", "sub_1", CancelMode("moonshot")); !errors.Is(err, ErrInvalidPlan) {
		t.Errorf("bad mode: want ErrInvalidPlan wrap, got %v", err)
	}
}

// TestCancelHandler_DefaultMode. Missing body OR empty mode
// defaults to end_of_period — matches the less-destructive
// default that Stripe / Chargebee use for the same endpoint. A
// user who POSTs {} shouldn't get their card auto-refunded.
func TestCancelHandler_DefaultMode(t *testing.T) {
	c := mollie.NewClient(mollie.Config{APIKey: "test_x", Env: mollie.EnvTest})
	svc := NewService(nil, c, Config{}, true)

	// Empty body: handler parses empty struct → Mode="", then
	// promotes to end_of_period → service tries pool → nil-panic.
	// So we can only verify the mode-defaulting path indirectly by
	// checking that an empty mode STRING doesn't hit the invalid-mode
	// branch (which would 400).
	req := httptest.NewRequest(http.MethodPost, "/platform/billing/subscriptions/sub_x/cancel", strings.NewReader(``))
	req = req.WithContext(auth.ContextWithClaims(req.Context(), &auth.Claims{Subject: "usr_1"}))
	w := httptest.NewRecorder()
	// Use a defer/recover to catch the nil-pool panic — the point
	// is that mode-defaulting works, not that a nil pool succeeds.
	defer func() {
		_ = recover()
	}()
	HandleCancelSubscription(svc).ServeHTTP(w, req)
	// If we got here without panic, the response body should NOT
	// be an invalid_mode 400 — that would prove mode-defaulting
	// broke.
	if w.Code == http.StatusBadRequest {
		var body map[string]string
		_ = json.NewDecoder(w.Body).Decode(&body)
		if body["error"] == "invalid_mode" {
			t.Errorf("empty body should default mode, not 400 invalid_mode")
		}
	}
}
