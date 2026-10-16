package functions

import (
	"context"
	"reflect"
	"testing"

	"github.com/eurobase/euroback/internal/tenant"
)

// Env var values are function secrets: a caller below developer gets the
// names only (HandleGet).
func TestWithholdEnvValues(t *testing.T) {
	fn := &EdgeFunction{Name: "hello", EnvVars: map[string]string{"STRIPE_KEY": "sk_live_x", "API_URL": "https://x"}}
	withholdEnvValues(fn)
	if fn.EnvVars != nil {
		t.Fatalf("values still present: %v", fn.EnvVars)
	}
	if want := []string{"API_URL", "STRIPE_KEY"}; !reflect.DeepEqual(fn.EnvVarNames, want) {
		t.Fatalf("names = %v, want %v", fn.EnvVarNames, want)
	}

	empty := &EdgeFunction{Name: "bare", EnvVars: map[string]string{}}
	withholdEnvValues(empty)
	if empty.EnvVars != nil || empty.EnvVarNames != nil {
		t.Fatalf("empty env: got %v / %v", empty.EnvVars, empty.EnvVarNames)
	}
}

func TestRedactForRole(t *testing.T) {
	for _, tc := range []struct {
		role       string
		wantValues bool
	}{
		{"", false}, // no role on the context: fail closed
		{"viewer", false},
		{"developer", true},
		{"admin", true},
		{"owner", true},
	} {
		fn := &EdgeFunction{Code: "export default () => 1", EnvVars: map[string]string{"K": "secret"}}
		ctx := context.Background()
		if tc.role != "" {
			ctx = tenant.WithRole(ctx, tc.role)
		}
		redactForRole(ctx, fn)
		if got := fn.EnvVars["K"] == "secret"; got != tc.wantValues {
			t.Errorf("role %q: values shown = %v, want %v", tc.role, got, tc.wantValues)
		}
		if gotCode := fn.Code != ""; gotCode != tc.wantValues || fn.CodeWithheld == tc.wantValues {
			t.Errorf("role %q: code shown = %v (withheld flag %v), want shown %v", tc.role, gotCode, fn.CodeWithheld, tc.wantValues)
		}
		if tc.wantValues && fn.EnvVarNames != nil {
			t.Errorf("role %q: names set alongside values: %v", tc.role, fn.EnvVarNames)
		}
	}
}
