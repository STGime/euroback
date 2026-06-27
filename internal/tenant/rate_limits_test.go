package tenant

import "testing"

// #225: per-project Rate Limits — verify the default + merge semantics
// without touching Redis or the HTTP layer. The merge contract is what
// every call-site downstream depends on:
//
//   * absent sub-object → all defaults
//   * partial overrides → unspecified knobs fill from defaults
//   * zero values are TREATED AS unset (so users can't accidentally
//     hammer themselves with "rate_limits.emails_per_hour = 0" — that
//     would be a footgun on the console). Setting "0" via API means
//     "back to default", not "block everything".
//   * trust_proxy is deliberate-set-only: false stays false, never
//     promoted to a default
// rateLimitsEqual compares two RateLimits values field-by-field. Direct
// `==` doesn't work since `TrustProxy *bool` is now a pointer; two
// equal-valued pointers from separate DefaultRateLimits() calls have
// different addresses.
func rateLimitsEqual(a, b RateLimits) bool {
	if a.SignupSigninPer5MinPerIP != b.SignupSigninPer5MinPerIP ||
		a.TokenRefreshPer5MinPerIP != b.TokenRefreshPer5MinPerIP ||
		a.TokenVerificationPer5MinPerIP != b.TokenVerificationPer5MinPerIP ||
		a.EmailsPerHour != b.EmailsPerHour ||
		a.SMSPerHour != b.SMSPerHour {
		return false
	}
	if (a.TrustProxy == nil) != (b.TrustProxy == nil) {
		return false
	}
	if a.TrustProxy != nil && *a.TrustProxy != *b.TrustProxy {
		return false
	}
	return true
}

func TestEffectiveRateLimits_NilConfig(t *testing.T) {
	var c *AuthConfig
	got := c.EffectiveRateLimits()
	want := DefaultRateLimits()
	if !rateLimitsEqual(got, want) {
		t.Fatalf("nil receiver should give DefaultRateLimits, got %+v want %+v", got, want)
	}
}

func TestEffectiveRateLimits_NilRateLimits(t *testing.T) {
	c := &AuthConfig{}
	got := c.EffectiveRateLimits()
	want := DefaultRateLimits()
	if !rateLimitsEqual(got, want) {
		t.Fatalf("nil RateLimits should give defaults, got %+v want %+v", got, want)
	}
}

func TestEffectiveRateLimits_PartialOverride(t *testing.T) {
	c := &AuthConfig{
		RateLimits: &RateLimits{
			SignupSigninPer5MinPerIP: 5, // tighten this knob only
		},
	}
	got := c.EffectiveRateLimits()
	if got.SignupSigninPer5MinPerIP != 5 {
		t.Errorf("explicit override lost: got %d, want 5", got.SignupSigninPer5MinPerIP)
	}
	defaults := DefaultRateLimits()
	if got.EmailsPerHour != defaults.EmailsPerHour {
		t.Errorf("unspecified knob did not fall back to default: got %d, want %d", got.EmailsPerHour, defaults.EmailsPerHour)
	}
	if got.SMSPerHour != defaults.SMSPerHour {
		t.Errorf("unspecified SMS knob did not fall back to default: got %d, want %d", got.SMSPerHour, defaults.SMSPerHour)
	}
}

func TestEffectiveRateLimits_ZeroMeansDefault(t *testing.T) {
	// A user clearing a field in the console sends 0 — that must mean
	// "back to default", not "zero allowed per period". The latter
	// would silently disable an entire auth flow.
	c := &AuthConfig{
		RateLimits: &RateLimits{
			EmailsPerHour: 0,
			SMSPerHour:    0,
		},
	}
	got := c.EffectiveRateLimits()
	defaults := DefaultRateLimits()
	if got.EmailsPerHour != defaults.EmailsPerHour {
		t.Errorf("EmailsPerHour=0 should map to default, got %d", got.EmailsPerHour)
	}
	if got.SMSPerHour != defaults.SMSPerHour {
		t.Errorf("SMSPerHour=0 should map to default, got %d", got.SMSPerHour)
	}
}

func TestEffectiveRateLimits_TrustProxyDefaultsAndOverrides(t *testing.T) {
	// TrustProxy is *bool so we can distinguish "absent" from
	// "explicit false". Four behaviours the per-project gates rely on:
	//
	//   1. Nil RateLimits → default false (safe-by-default Supabase
	//      parity; the verification-pending follow-up tracks flipping
	//      this once the Scaleway LB ↔ nginx XFF chain is confirmed).
	//   2. Empty RateLimits, no trust_proxy field → same default.
	//   3. Explicit false in storage → false (no surprise).
	//   4. Explicit true in storage → true (project opted in after
	//      confirming the ingress trusts XFF).
	falsePtr := false
	truePtr := true

	cNil := &AuthConfig{}
	if got := cNil.EffectiveRateLimits().TrustProxy; got == nil || *got {
		t.Errorf("TrustProxy must default to false on nil RateLimits, got %v", got)
	}

	cAbsentField := &AuthConfig{RateLimits: &RateLimits{}}
	if got := cAbsentField.EffectiveRateLimits().TrustProxy; got == nil || *got {
		t.Errorf("TrustProxy must default to false when the field is absent, got %v", got)
	}

	cExplicitFalse := &AuthConfig{RateLimits: &RateLimits{TrustProxy: &falsePtr}}
	if got := cExplicitFalse.EffectiveRateLimits().TrustProxy; got == nil || *got {
		t.Errorf("explicit TrustProxy=false must stay false, got %v", got)
	}

	cExplicitTrue := &AuthConfig{RateLimits: &RateLimits{TrustProxy: &truePtr}}
	if got := cExplicitTrue.EffectiveRateLimits().TrustProxy; got == nil || !*got {
		t.Errorf("explicit TrustProxy=true must pass through, got %v", got)
	}
}

func TestDefaultRateLimits_Numbers(t *testing.T) {
	// Snapshot of the published defaults so a future change shows up
	// in code review. These numbers go into the docs page in #230 — if
	// they shift, the doc table needs to shift with them.
	d := DefaultRateLimits()
	expect := map[string]int{
		// Held at 8 (≈96/h) under the #234 review — the email cap
		// that would have bounded amplification at 360/h is deferred
		// until BYO-SMTP exists. Bump back to 30 (Supabase parity)
		// then.
		"signup_signin_per_5min_per_ip":      8,
		"token_refresh_per_5min_per_ip":      150,
		"token_verification_per_5min_per_ip": 30,
		"emails_per_hour":                    2,
		"sms_per_hour":                       30,
	}
	got := map[string]int{
		"signup_signin_per_5min_per_ip":     d.SignupSigninPer5MinPerIP,
		"token_refresh_per_5min_per_ip":     d.TokenRefreshPer5MinPerIP,
		"token_verification_per_5min_per_ip": d.TokenVerificationPer5MinPerIP,
		"emails_per_hour":                   d.EmailsPerHour,
		"sms_per_hour":                      d.SMSPerHour,
	}
	for k, want := range expect {
		if got[k] != want {
			t.Errorf("default %s: got %d, want %d", k, got[k], want)
		}
	}
	if d.TrustProxy == nil || *d.TrustProxy {
		t.Errorf("default TrustProxy must be false (Supabase parity, safe-by-default), got %v", d.TrustProxy)
	}
}

// JSON round-trip: a stored auth_config with rate_limits must parse
// back into a struct that EffectiveRateLimits can read. Catches future
// renames of the JSON tags.
func TestParseAuthConfig_RateLimitsRoundTrip(t *testing.T) {
	raw := []byte(`{
		"providers":{"email_password":{"enabled":true}},
		"password_min_length":8,
		"session_duration":"168h",
		"redirect_urls":["http://localhost:3000"],
		"rate_limits":{
			"signup_signin_per_5min_per_ip":7,
			"emails_per_hour":3,
			"trust_proxy":true
		}
	}`)
	cfg := ParseAuthConfig(raw)
	if cfg.RateLimits == nil {
		t.Fatal("ParseAuthConfig dropped rate_limits sub-object")
	}
	eff := cfg.EffectiveRateLimits()
	if eff.SignupSigninPer5MinPerIP != 7 {
		t.Errorf("SignupSigninPer5MinPerIP: got %d, want 7", eff.SignupSigninPer5MinPerIP)
	}
	if eff.EmailsPerHour != 3 {
		t.Errorf("EmailsPerHour: got %d, want 3", eff.EmailsPerHour)
	}
	if eff.TrustProxy == nil || !*eff.TrustProxy {
		t.Errorf("TrustProxy: want true (explicit override), got %v", eff.TrustProxy)
	}
	// Unspecified knob falls back to default.
	if eff.SMSPerHour != DefaultRateLimits().SMSPerHour {
		t.Errorf("SMSPerHour: got %d, want default %d", eff.SMSPerHour, DefaultRateLimits().SMSPerHour)
	}
}
