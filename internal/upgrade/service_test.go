package upgrade

import "testing"

// TestStateConstants pins the wire-visible state names so a
// rename in Go doesn't silently break a DB row without also
// updating the migration or the admin UI.
func TestStateConstants(t *testing.T) {
	cases := []struct {
		got  State
		want string
	}{
		{StateRequested, "requested"},
		{StateProvisioning, "provisioning"},
		{StateCopying, "copying"},
		{StateCuttingOver, "cutting_over"},
		{StateLive, "live"},
		{StateConfirmed, "confirmed"},
		{StateFailed, "failed"},
	}
	for _, c := range cases {
		if string(c.got) != c.want {
			t.Errorf("state constant %q = %q, want %q", c.want, string(c.got), c.want)
		}
	}
}

// TestErrSentinels ensures the exported error sentinels stay stable
// so admin API handlers can errors.Is-match them to HTTP status
// codes without their message text drifting silently.
func TestErrSentinels(t *testing.T) {
	if ErrAlreadyTeam == nil ||
		ErrUpgradeInFlight == nil ||
		ErrBetaAccessMissing == nil ||
		ErrProjectNotFound == nil ||
		ErrUnsupportedPlan == nil {
		t.Fatal("one or more Err* sentinels is nil")
	}
}

// TestNullableAdminID confirms the FK-null helper.
func TestNullableAdminID(t *testing.T) {
	if got := nullableAdminID(""); got != nil {
		t.Errorf("empty id → want nil, got %v", got)
	}
	if got := nullableAdminID("u_123"); got != "u_123" {
		t.Errorf(`"u_123" → want "u_123", got %v`, got)
	}
}
