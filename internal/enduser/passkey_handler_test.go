package enduser

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eurobase/euroback/internal/auth"
)

const passkeyEnabledCfg = `{"providers":{"email_password":{"enabled":true}},"passkeys":{"enabled":true,"rp_id":"app.example.com","origins":["https://app.example.com"]}}`

// Enrol/manage endpoints require an authenticated end user: with a project
// context but no end-user claims, they must 401 before touching the service.
func TestPasskeyManage_RequiresAuth(t *testing.T) {
	handlers := map[string]http.HandlerFunc{
		"register_begin":  HandlePasskeyRegisterBegin(&AuthService{}),
		"register_finish": HandlePasskeyRegisterFinish(&AuthService{}),
		"list":            HandleListPasskeys(&AuthService{}),
		"delete":          HandleDeletePasskey(&AuthService{}),
	}
	for name, h := range handlers {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/auth/passkey/x", strings.NewReader("{}"))
			pc := &auth.ProjectContext{ProjectID: "prj", SchemaName: "tenant_x", JWTSecret: "s", AuthConfig: json.RawMessage(passkeyEnabledCfg)}
			req = req.WithContext(auth.ContextWithProject(req.Context(), pc))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("%s: want 401 without end-user claims, got %d (%s)", name, w.Code, w.Body.String())
			}
		})
	}
}

// Login begin is public (no end-user JWT) and stateless (no DB): it returns a
// challenge when passkeys are enabled, and 400 when they are not.
func TestPasskeyLoginBegin(t *testing.T) {
	svc := &AuthService{}

	// Enabled → 200 + options.
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/passkey/login/begin", nil)
	pc := &auth.ProjectContext{ProjectID: "prj", SchemaName: "tenant_x", JWTSecret: "project-secret", AuthConfig: json.RawMessage(passkeyEnabledCfg)}
	req = req.WithContext(auth.ContextWithProject(req.Context(), pc))
	w := httptest.NewRecorder()
	HandlePasskeyLoginBegin(svc).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("enabled: want 200, got %d (%s)", w.Code, w.Body.String())
	}
	var ch auth.PasskeyChallenge
	if err := json.Unmarshal(w.Body.Bytes(), &ch); err != nil || ch.ChallengeID == "" || len(ch.Options) == 0 {
		t.Fatalf("enabled: bad challenge %s (err %v)", w.Body.String(), err)
	}

	// Disabled → 400.
	req2 := httptest.NewRequest(http.MethodPost, "/v1/auth/passkey/login/begin", nil)
	pc2 := &auth.ProjectContext{ProjectID: "prj", SchemaName: "tenant_x", JWTSecret: "s", AuthConfig: json.RawMessage(`{"providers":{"email_password":{"enabled":true}}}`)}
	req2 = req2.WithContext(auth.ContextWithProject(req2.Context(), pc2))
	w2 := httptest.NewRecorder()
	HandlePasskeyLoginBegin(svc).ServeHTTP(w2, req2)
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("disabled: want 400, got %d (%s)", w2.Code, w2.Body.String())
	}
}
