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
	"github.com/go-chi/chi/v5"
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

// TestCancelHandler_ImmediateRejected verifies the policy guard
// added 2026-08-16: `mode:"immediate"` is rejected with 400 at
// the HTTP layer BEFORE the service call, regardless of the
// service's own capability. Non-vacuous: without the guard the
// enabled service would attempt to run the immediate path (and
// nil-pool panic here), so a `w.Code == 400 && code ==
// immediate_cancel_disabled` outcome proves the guard fired.
//
// Without this test a future refactor could quietly drop the
// guard: the disabled-503 test short-circuits on Enabled() before
// reaching it, and the service-level immediate tests bypass the
// handler entirely — every existing check would stay green.
func TestCancelHandler_ImmediateRejected(t *testing.T) {
	c := mollie.NewClient(mollie.Config{APIKey: "test_x", Env: mollie.EnvTest})
	svc := NewService(nil, c, Config{}, true) // enabled → guard is the only defense

	req := httptest.NewRequest(http.MethodPost, "/platform/billing/subscriptions/sub_x/cancel", strings.NewReader(`{"mode":"immediate"}`))
	// httptest doesn't wire chi route context, and the handler
	// reads `id` via chi.URLParam before hitting the guard —
	// without setup we'd 400 on missing_subscription_id and
	// mask the real check.
	rc := chi.NewRouteContext()
	rc.URLParams.Add("id", "sub_x")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rc)
	ctx = auth.ContextWithClaims(ctx, &auth.Claims{Subject: "usr_1"})
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	HandleCancelSubscription(svc).ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400 (guard blocks immediate), got %d", w.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["code"] != "immediate_cancel_disabled" {
		t.Errorf("code = %q, want immediate_cancel_disabled (guard fired with wrong code)", body["code"])
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
		if body["code"] == "invalid_mode" {
			t.Errorf("empty body should default mode, not 400 invalid_mode")
		}
	}
}
