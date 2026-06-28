package compliance

import (
	"os"
	"testing"
)

// TestLoadResidencyConfigFromEnv pins the env-var contract for #173. The
// DPA report's truthfulness depends on these three knobs being read
// honestly from the deployment, so the parser MUST:
//
//  * default to production-shipped values when env vars are absent
//    (so a misconfigured pod doesn't blank out the report);
//  * accept the operator-typed booleans the runbook documents
//    (1/true/yes/on, 0/false/no/off);
//  * pass through unknown TLS_MIN strings unchanged so a future
//    "TLS 1.4" or "TLS 1.3+QUIC" doesn't need a code change.
func TestLoadResidencyConfigFromEnv(t *testing.T) {
	// Snapshot any pre-existing values so the test doesn't poison
	// adjacent test cases.
	saved := map[string]string{}
	for _, k := range []string{"RESIDENCY_REGION", "ENCRYPTION_AT_REST", "TLS_MIN"} {
		if v, ok := os.LookupEnv(k); ok {
			saved[k] = v
		}
		os.Unsetenv(k)
	}
	t.Cleanup(func() {
		for _, k := range []string{"RESIDENCY_REGION", "ENCRYPTION_AT_REST", "TLS_MIN"} {
			os.Unsetenv(k)
		}
		for k, v := range saved {
			os.Setenv(k, v)
		}
	})

	t.Run("absent → defaults", func(t *testing.T) {
		got := LoadResidencyConfigFromEnv()
		want := DefaultResidencyConfig()
		if got != want {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})

	t.Run("operator overrides each knob independently", func(t *testing.T) {
		os.Setenv("RESIDENCY_REGION", "Germany (Hetzner DC-FSN1)")
		os.Setenv("ENCRYPTION_AT_REST", "false")
		os.Setenv("TLS_MIN", "TLS 1.2")
		defer os.Unsetenv("RESIDENCY_REGION")
		defer os.Unsetenv("ENCRYPTION_AT_REST")
		defer os.Unsetenv("TLS_MIN")

		got := LoadResidencyConfigFromEnv()
		if got.StorageLocation != "Germany (Hetzner DC-FSN1)" {
			t.Errorf("StorageLocation = %q", got.StorageLocation)
		}
		if got.EncryptionAtRest {
			t.Error("EncryptionAtRest = true, want false (operator said so)")
		}
		if got.TLSMin != "TLS 1.2" {
			t.Errorf("TLSMin = %q", got.TLSMin)
		}
	})

	t.Run("empty TLS_MIN surfaces 'no floor enforced'", func(t *testing.T) {
		os.Setenv("TLS_MIN", "")
		defer os.Unsetenv("TLS_MIN")
		got := LoadResidencyConfigFromEnv()
		if got.TLSMin != "" {
			t.Errorf("TLSMin = %q, want empty (operator explicitly cleared)", got.TLSMin)
		}
	})

	t.Run("typo'd ENCRYPTION_AT_REST falls back to default rather than panicking", func(t *testing.T) {
		os.Setenv("ENCRYPTION_AT_REST", "yess")
		defer os.Unsetenv("ENCRYPTION_AT_REST")
		got := LoadResidencyConfigFromEnv()
		if got.EncryptionAtRest != true {
			t.Error("typo should fall back to default true, got false")
		}
	})

	// Closes review #3 on #244: an out-of-allowlist TLS_MIN must NOT
	// pass through as-is. Otherwise the DPA report could emit a
	// truthful-shaped lie like `encryption_in_transit: true,
	// tls_min: "garbage"`, which is harder to spot in an audit than
	// a missing value.
	t.Run("garbage TLS_MIN normalises to empty", func(t *testing.T) {
		os.Setenv("TLS_MIN", "TLSv1.3")  // close-but-wrong format
		defer os.Unsetenv("TLS_MIN")
		got := LoadResidencyConfigFromEnv()
		if got.TLSMin != "" {
			t.Errorf("garbage TLS_MIN should normalise to empty, got %q", got.TLSMin)
		}
	})

	t.Run("allowlisted TLS_MIN values pass through", func(t *testing.T) {
		for _, v := range []string{"TLS 1.2", "TLS 1.3"} {
			os.Setenv("TLS_MIN", v)
			got := LoadResidencyConfigFromEnv()
			if got.TLSMin != v {
				t.Errorf("TLS_MIN=%q normalised to %q, want passthrough", v, got.TLSMin)
			}
			os.Unsetenv("TLS_MIN")
		}
	})
}

// TestParseBoolish documents the accepted spellings so future operators
// don't have to read the parser to know what works.
func TestParseBoolish(t *testing.T) {
	cases := []struct {
		in   string
		dflt bool
		want bool
	}{
		{"true", false, true},
		{"True", false, true},
		{"TRUE", false, true},
		{"yes", false, true},
		{"on", false, true},
		{"1", false, true},
		{"false", true, false},
		{"FALSE", true, false},
		{"no", true, false},
		{"off", true, false},
		{"0", true, false},
		{" true ", false, true}, // operator with sloppy quoting
		{"", false, false},      // empty falls through to default
		{"", true, true},
		{"maybe", true, true},   // unparseable falls through
		{"maybe", false, false},
	}
	for _, c := range cases {
		if got := parseBoolish(c.in, c.dflt); got != c.want {
			t.Errorf("parseBoolish(%q, %v) = %v, want %v", c.in, c.dflt, got, c.want)
		}
	}
}

// TestBuildDataFlowInfo_Truthful pins the #173 truthfulness invariant
// end-to-end through the actual production code path. Pre-refactor we
// re-stated the boolean derivation in the test, which would have
// silently passed if a future change put back `EncryptionInTransit:
// true` hardcoded in GenerateReport. Now we call the same
// BuildDataFlowInfo the report path uses, so a regression there fails
// here.
//
// The contract: encryption_in_transit ⇔ TLSMin is in the closed
// allowlist (covered by LoadResidencyConfigFromEnv normalisation), and
// tls_min round-trips into the report verbatim.
func TestBuildDataFlowInfo_Truthful(t *testing.T) {
	cases := []struct {
		name    string
		cfg     ResidencyConfig
		cross   bool
		wantInT bool
		wantMin string
	}{
		{
			name:    "production: FR + at-rest + TLS 1.3",
			cfg:     DefaultResidencyConfig(),
			wantInT: true,
			wantMin: "TLS 1.3",
		},
		{
			name:    "dev with no TLS floor → in_transit false",
			cfg:     ResidencyConfig{StorageLocation: "dev", EncryptionAtRest: false, TLSMin: ""},
			wantInT: false,
			wantMin: "",
		},
		{
			name:    "TLS 1.2 floor still counts as in-transit encryption",
			cfg:     ResidencyConfig{StorageLocation: "FR", EncryptionAtRest: true, TLSMin: "TLS 1.2"},
			wantInT: true,
			wantMin: "TLS 1.2",
		},
		{
			name:    "cross-border flag forwards through",
			cfg:     DefaultResidencyConfig(),
			cross:   true,
			wantInT: true,
			wantMin: "TLS 1.3",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := BuildDataFlowInfo(c.cfg, c.cross)
			if got.EncryptionInTransit != c.wantInT {
				t.Errorf("EncryptionInTransit=%v, want %v", got.EncryptionInTransit, c.wantInT)
			}
			if got.TLSMin != c.wantMin {
				t.Errorf("TLSMin=%q, want %q", got.TLSMin, c.wantMin)
			}
			if got.EncryptionAtRest != c.cfg.EncryptionAtRest {
				t.Errorf("EncryptionAtRest=%v, want %v (passthrough)", got.EncryptionAtRest, c.cfg.EncryptionAtRest)
			}
			if got.StorageLocation != c.cfg.StorageLocation {
				t.Errorf("StorageLocation=%q, want %q (passthrough)", got.StorageLocation, c.cfg.StorageLocation)
			}
			if got.CrossBorderTransfers != c.cross {
				t.Errorf("CrossBorderTransfers=%v, want %v", got.CrossBorderTransfers, c.cross)
			}
		})
	}
}
