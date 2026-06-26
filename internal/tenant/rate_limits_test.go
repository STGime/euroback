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
func TestEffectiveRateLimits_NilConfig(t *testing.T) {
	var c *AuthConfig
	got := c.EffectiveRateLimits()
	want := DefaultRateLimits()
	if got != want {
		t.Fatalf("nil receiver should give DefaultRateLimits, got %+v want %+v", got, want)
	}
}

func TestEffectiveRateLimits_NilRateLimits(t *testing.T) {
	c := &AuthConfig{}
	got := c.EffectiveRateLimits()
	want := DefaultRateLimits()
	if got != want {
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

func TestEffectiveRateLimits_TrustProxyStaysOptIn(t *testing.T) {
	// TrustProxy is NOT default-merged: an absent / false value means
	// "do not trust X-Forwarded-For". Promoting it to a default would
	// be a privacy/security regression for projects without an edge.
	cNil := &AuthConfig{}
	if cNil.EffectiveRateLimits().TrustProxy {
		t.Error("TrustProxy must default to false on nil RateLimits")
	}
	cExplicitFalse := &AuthConfig{RateLimits: &RateLimits{TrustProxy: false}}
	if cExplicitFalse.EffectiveRateLimits().TrustProxy {
		t.Error("explicit TrustProxy=false must stay false (not promoted)")
	}
	cTrue := &AuthConfig{RateLimits: &RateLimits{TrustProxy: true}}
	if !cTrue.EffectiveRateLimits().TrustProxy {
		t.Error("explicit TrustProxy=true must pass through")
	}
}

func TestDefaultRateLimits_Numbers(t *testing.T) {
	// Snapshot of the published defaults so a future change shows up
	// in code review. These numbers go into the docs page in #230 — if
	// they shift, the doc table needs to shift with them.
	d := DefaultRateLimits()
	expect := map[string]int{
		"signup_signin_per_5min_per_ip":     30,
		"token_refresh_per_5min_per_ip":     150,
		"token_verification_per_5min_per_ip": 30,
		"emails_per_hour":                   2,
		"sms_per_hour":                      30,
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
	if d.TrustProxy {
		t.Error("default TrustProxy must be false (safe default)")
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
	if !eff.TrustProxy {
		t.Error("TrustProxy: got false, want true")
	}
	// Unspecified knob falls back to default.
	if eff.SMSPerHour != DefaultRateLimits().SMSPerHour {
		t.Errorf("SMSPerHour: got %d, want default %d", eff.SMSPerHour, DefaultRateLimits().SMSPerHour)
	}
}
