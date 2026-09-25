package tenantlogin

import (
	"regexp"
	"testing"
)

// Pinned vector — functions-runner/tenant_db_test.ts asserts the same
// value, so Go (sets the password) and the runner (connects with it)
// can't drift apart.
func TestFuncPasswordVector(t *testing.T) {
	got := FuncPassword([]byte("0123456789abcdef0123456789abcdef"), "tenant_abc")
	const want = "5f64bdf519eef7742b69146d0b0d9a1949f07a14b5292d5373ffce01ad74a95b"
	if got != want {
		t.Fatalf("FuncPassword vector changed: got %s want %s", got, want)
	}
}

func TestNewEnsurerSecretRules(t *testing.T) {
	if e, err := NewEnsurer(nil, "eurobase", nil); e != nil || err != nil {
		t.Fatalf("empty secret must disable without error, got %v %v", e, err)
	}
	if _, err := NewEnsurer(nil, "eurobase", []byte("short")); err == nil {
		t.Fatal("short secret must be rejected")
	}
}

func TestSchemaValidation(t *testing.T) {
	for _, s := range []string{"public", "tenant_", "tenant_x;drop", "Tenant_ab"} {
		if schemaRe.MatchString(s) {
			t.Errorf("schema %q should be rejected", s)
		}
	}
	if !schemaRe.MatchString("tenant_0515e4e2_e195_4018_ab19_f18aae213e2a") {
		t.Error("real tenant schema rejected")
	}
}

func TestScramVerifierShapeAndDeterminism(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	v1, err := ScramVerifier(secret, "tenant_abc")
	if err != nil {
		t.Fatal(err)
	}
	v2, _ := ScramVerifier(secret, "tenant_abc")
	if v1 != v2 {
		t.Fatal("verifier must be deterministic")
	}
	if !regexp.MustCompile(`^SCRAM-SHA-256\$4096:[A-Za-z0-9+/=]+\$[A-Za-z0-9+/=]+:[A-Za-z0-9+/=]+$`).MatchString(v1) {
		t.Fatalf("unexpected verifier shape: %s", v1)
	}
	if other, _ := ScramVerifier(secret, "tenant_abd"); other == v1 {
		t.Fatal("different schemas must get different verifiers")
	}
}
