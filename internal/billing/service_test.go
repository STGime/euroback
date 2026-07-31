package billing

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/eurobase/euroback/internal/billing/mollie"
)

// mustNewMollieClient wires the Mollie client to a caller-provided
// fake server so tests can script the customer + payment endpoints.
// Kept minimal — the client itself is exercised in
// internal/billing/mollie/client_test.go.
func mustNewMollieClient(t *testing.T, handler http.HandlerFunc) (*mollie.Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := mollie.NewClient(mollie.Config{
		APIKey:  "test_x",
		Env:     mollie.EnvTest,
		BaseURL: srv.URL,
	})
	return c, srv
}

// TestService_DisabledShortCircuits documents the fail-closed shape:
// with enabled=false the service refuses without touching the pool
// or Mollie. Handler mirrors this by returning 503.
func TestService_DisabledShortCircuits(t *testing.T) {
	c, _ := mustNewMollieClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Mollie must not be called when billing disabled")
	})
	// nil pool is safe here because the disabled branch fires
	// before any DB access.
	svc := NewService(nil, c, Config{}, false)
	if svc.Enabled() {
		t.Fatal("Enabled() must be false when constructed with enabled=false")
	}
	_, err := svc.CreateCheckout(context.Background(), "usr_1", "proj_1", "pro")
	if !errors.Is(err, ErrBillingDisabled) {
		t.Errorf("want ErrBillingDisabled, got %v", err)
	}
}

// TestService_InvalidPlan covers the two invalid-plan branches:
// unknown code and the deliberately-refused "team" (schema present,
// billing not shipped). Guard against a future PR silently enabling
// team billing without a matching invoice flow.
func TestService_InvalidPlan(t *testing.T) {
	c, _ := mustNewMollieClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Mollie must not be called for invalid plan")
	})
	svc := NewService(nil, c, Config{}, true)

	for _, plan := range []string{"", "enterprise", "hobby"} {
		t.Run("unknown:"+plan, func(t *testing.T) {
			_, err := svc.CreateCheckout(context.Background(), "usr_1", "proj_1", plan)
			if !errors.Is(err, ErrInvalidPlan) {
				t.Errorf("plan=%q want ErrInvalidPlan, got %v", plan, err)
			}
		})
	}

	t.Run("team is refused until billing ships", func(t *testing.T) {
		_, err := svc.CreateCheckout(context.Background(), "usr_1", "proj_1", "team")
		if !errors.Is(err, ErrInvalidPlan) {
			t.Errorf("team should return ErrInvalidPlan until it ships, got %v", err)
		}
	})
}

// TestPlanPriceCents locks the public pricing table so a copy-paste
// mistake ("Pro is 1900" → "Pro is 190") shows up as a test failure
// rather than as a €1.90 charge in production.
func TestPlanPriceCents(t *testing.T) {
	if planPriceCents["pro"] != 1900 {
		t.Errorf("Pro price drifted: got %d cents, want 1900 (€19)", planPriceCents["pro"])
	}
	if planPriceCents["team"] != 14900 {
		t.Errorf("Team price drifted: got %d cents, want 14900 (€149)", planPriceCents["team"])
	}
	if _, ok := planPriceCents["free"]; ok {
		t.Error("free must never appear in planPriceCents — Free is not chargeable")
	}
}

// The rest of CreateCheckout — ownership check, race translation,
// customer lazy-create, payment insert — needs a live Postgres. It's
// exercised by the ship-gate manual QA (per PR body) plus the
// integration tests that PR 4 adds when the webhook lands. Keeping
// this file focused on branches the tx-less path can hit means unit
// tests stay fast and don't require containers.
