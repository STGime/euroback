package sms

import (
	"testing"
)

// TestMaxOTPAttempts pins the per-token guess budget (#233). This is
// the constant that turns "attacker gets ~10^6 tries per issued code"
// into "attacker gets 5 tries per issued code per phone." Changing it
// alters the security posture and warrants a review, so we fail loudly
// if someone bumps it without touching this test.
//
// The upper safety bound: at 6 tries a fresh 6-digit code gives an
// attacker a ~6/10^6 ≈ 6e-6 hit rate per issued code. Anything above
// ~10 starts erasing the security benefit of the fix.
func TestMaxOTPAttempts(t *testing.T) {
	if maxOTPAttempts != 5 {
		t.Errorf("maxOTPAttempts = %d, want 5. Changing this alters the #233 threat model — update the test AND get a security review before merging.", maxOTPAttempts)
	}
	if maxOTPAttempts > 10 {
		t.Errorf("maxOTPAttempts = %d exceeds the safety bound (10). At this cap the per-token limit no longer meaningfully constrains brute force against a 6-digit code.", maxOTPAttempts)
	}
}
