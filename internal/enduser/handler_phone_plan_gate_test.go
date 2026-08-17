package enduser

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eurobase/euroback/internal/auth"
)

// TestPhoneOTP_PlanGate pins the #329 paid-plan gate on the two
// phone-auth endpoints. Free-plan (and any unknown plan) must 402
// with `code:"paid_plan_required"` BEFORE any DB, rate-limit, or
// SMS-provider work — the gate is the whole point, so it must fire
// first.
//
// Why 402 and not 403: the semantic HTTP code for "payment required
// to proceed" cleanly distinguishes an upgrade-nudge from a config
// error (400) or missing auth (401). Downstream SDK clients can
// branch on the status code without parsing the error message.
//
// The Handle* funcs take an AuthService pointer, but the gate runs
// before any service call, so `nil` is safe here — a regression that
// moves the gate below the service call would nil-panic and fail
// the test loudly rather than silently green.
func TestPhoneOTP_PlanGate(t *testing.T) {
	// Enable phone provider in the auth_config so the "phone auth
	// not enabled" 400 branch doesn't mask the plan gate.
	authCfg := json.RawMessage(`{"providers":{"phone":{"enabled":true}}}`)

	cases := []struct {
		name    string
		plan    string
		wantGate bool // true = expect 402 paid_plan_required
	}{
		{"free plan blocked", "free", true},
		{"empty plan blocked (fail-closed)", "", true},
		{"unknown plan blocked (fail-closed)", "enterprise-2028", true},
		{"pro plan passes gate", "pro", false},
		{"team plan passes gate", "team", false},
		{"legal_team plan passes gate", "legal_team", false},
	}

	endpoints := []struct {
		name    string
		path    string
		body    string
		handler func() http.HandlerFunc
	}{
		{
			name:    "send-otp",
			path:    "/v1/auth/phone/send-otp",
			body:    `{"phone":"+33612345678"}`,
			handler: func() http.HandlerFunc { return HandleSendPhoneOTP(nil) },
		},
		{
			name:    "verify",
			path:    "/v1/auth/phone/verify",
			body:    `{"phone":"+33612345678","code":"123456"}`,
			handler: func() http.HandlerFunc { return HandleVerifyPhoneOTP(nil) },
		},
	}

	for _, ep := range endpoints {
		for _, tc := range cases {
			t.Run(ep.name+"/"+tc.name, func(t *testing.T) {
				req := httptest.NewRequest(http.MethodPost, ep.path, strings.NewReader(ep.body))
				pc := &auth.ProjectContext{
					ProjectID:  "prj_test",
					SchemaName: "tenant_test",
					Plan:       tc.plan,
					AuthConfig: authCfg,
				}
				req = req.WithContext(auth.ContextWithProject(req.Context(), pc))
				w := httptest.NewRecorder()

				// Recover — for the paid-plan branches, the handler
				// will proceed past the gate and (with nil service)
				// nil-panic downstream. That IS the pass signal: the
				// gate didn't fire.
				defer func() { _ = recover() }()
				ep.handler().ServeHTTP(w, req)

				if tc.wantGate {
					if w.Code != http.StatusPaymentRequired {
						t.Fatalf("plan=%q: want 402, got %d body=%s", tc.plan, w.Code, w.Body.String())
					}
					var body map[string]any
					if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
						t.Fatalf("decode: %v", err)
					}
					if body["code"] != "paid_plan_required" {
						t.Errorf("plan=%q: code = %v, want paid_plan_required", tc.plan, body["code"])
					}
				} else {
					if w.Code == http.StatusPaymentRequired {
						t.Fatalf("plan=%q: gate fired for paid plan (should have passed), body=%s", tc.plan, w.Body.String())
					}
				}
			})
		}
	}
}

// TestPhoneOTP_MissingProjectContext confirms the unauth branch runs
// BEFORE the plan gate — a request without ProjectContext must 401,
// not 402. Otherwise leaking "paid_plan_required" pre-auth would
// tell an attacker the project exists on a Free plan.
func TestPhoneOTP_MissingProjectContext(t *testing.T) {
	for _, ep := range []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"send-otp", HandleSendPhoneOTP(nil)},
		{"verify", HandleVerifyPhoneOTP(nil)},
	} {
		t.Run(ep.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/auth/phone/"+ep.name, strings.NewReader(`{}`))
			w := httptest.NewRecorder()
			ep.handler.ServeHTTP(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Errorf("want 401, got %d", w.Code)
			}
		})
	}
}
