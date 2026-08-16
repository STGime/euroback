package billing

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/billing/mollie"
)

// bootHandler wires the handler against a service in a given
// enabled/disabled state. Kept as a helper because every test needs
// the same shape (service + auth-injected request).
func bootHandler(t *testing.T, enabled bool) http.Handler {
	t.Helper()
	c := mollie.NewClient(mollie.Config{APIKey: "test_x", Env: mollie.EnvTest})
	svc := NewService(nil, c, Config{}, enabled)
	return HandleCreateCheckout(svc)
}

// withClaims returns a request context that carries a fake
// authenticated user, matching what the platform auth middleware
// would produce.
func withClaims(r *http.Request, subject string) *http.Request {
	ctx := auth.ContextWithClaims(r.Context(), &auth.Claims{
		Subject: subject,
		Email:   subject + "@example.com",
	})
	return r.WithContext(ctx)
}

func TestHandler_DisabledReturns503(t *testing.T) {
	h := bootHandler(t, false)
	req := httptest.NewRequest(http.MethodPost, "/platform/billing/checkout", strings.NewReader(`{"project_id":"p","plan_code":"pro"}`))
	req = withClaims(req, "usr_1")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("want 503 when disabled, got %d", w.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Envelope shape: {"error": <human>, "code": <machine>} — see
	// writeJSONError comment. Test asserts on the machine-readable
	// code field, not the human-readable message.
	if body["code"] != "billing_disabled" {
		t.Errorf("code = %q, want billing_disabled", body["code"])
	}
}

func TestHandler_UnauthenticatedReturns401(t *testing.T) {
	h := bootHandler(t, true)
	req := httptest.NewRequest(http.MethodPost, "/platform/billing/checkout", strings.NewReader(`{"project_id":"p","plan_code":"pro"}`))
	// no claims — leaving the context empty.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("want 401 when unauthenticated, got %d", w.Code)
	}
}

func TestHandler_MissingFieldsReturn400(t *testing.T) {
	h := bootHandler(t, true)
	cases := []struct {
		name string
		body string
		want string
	}{
		{"empty body", ``, "invalid_body"},
		{"garbage", `not json`, "invalid_body"},
		{"missing project_id", `{"plan_code":"pro"}`, "missing_project_id"},
		{"missing plan_code", `{"project_id":"p"}`, "missing_plan_code"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/platform/billing/checkout", strings.NewReader(tt.body))
			req = withClaims(req, "usr_1")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Errorf("want 400, got %d", w.Code)
			}
			var body map[string]string
			_ = json.NewDecoder(w.Body).Decode(&body)
			if body["code"] != tt.want {
				t.Errorf("code = %q, want %q", body["code"], tt.want)
			}
		})
	}
}

func TestHandler_InvalidPlanReturns400(t *testing.T) {
	h := bootHandler(t, true)
	req := httptest.NewRequest(http.MethodPost, "/platform/billing/checkout", strings.NewReader(`{"project_id":"p","plan_code":"enterprise"}`))
	req = withClaims(req, "usr_1")
	// nil pool would panic in the ownership check but invalid
	// plan is validated before the pool touch — safe to run here.
	req = req.WithContext(context.Background())
	req = withClaims(req, "usr_1")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400 for unknown plan, got %d", w.Code)
	}
}
