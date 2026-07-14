package plans

import "testing"

// Phase B of the public-beta launch plan. `legacyFreeLimits` is the
// pure helper that GetEffectiveProjectLimits uses to keep existing
// Free projects on the pre-Phase-B numbers during their 90-day
// grandfather window. Pinned as a unit test so a future edit to the
// halved-cap values doesn't drift the grandfather target too.

func TestLegacyFreeLimits_RestoresPrePhaseBCaps(t *testing.T) {
	// Simulate the CURRENT (post-migration-000075) Free row: halved
	// caps plus the three new binary Pro-only gates set to false.
	current := &PlanLimits{
		Plan:           "free",
		MAULimit:       5000, // halved
		StorageMB:      512,  // halved
		BandwidthMB:    2048, // halved
		WSConnections:  50,   // halved
		DBSizeMB:       500,  // unchanged
		RateLimitRPS:   100,  // unchanged
		UploadSizeMB:   10,   // unchanged
		CustomDomain:   false,
		BYOSMTP:        false,
		QuotaAlerts:    false,
	}
	got := legacyFreeLimits(current)

	// The four caps Phase B halved should be restored.
	if got.MAULimit != 10000 {
		t.Errorf("MAULimit = %d, want 10000", got.MAULimit)
	}
	if got.StorageMB != 1024 {
		t.Errorf("StorageMB = %d, want 1024", got.StorageMB)
	}
	if got.BandwidthMB != 5120 {
		t.Errorf("BandwidthMB = %d, want 5120", got.BandwidthMB)
	}
	if got.WSConnections != 100 {
		t.Errorf("WSConnections = %d, want 100", got.WSConnections)
	}

	// Untouched caps pass through unchanged.
	if got.DBSizeMB != 500 {
		t.Errorf("DBSizeMB drifted: %d", got.DBSizeMB)
	}
	if got.RateLimitRPS != 100 {
		t.Errorf("RateLimitRPS drifted: %d", got.RateLimitRPS)
	}
	if got.UploadSizeMB != 10 {
		t.Errorf("UploadSizeMB drifted: %d", got.UploadSizeMB)
	}

	// Binary Pro-only gates MUST stay off — grandfathering restores
	// the OLD caps, not new features. Enabling BYO SMTP for a
	// grandfathered Free project would be a real product change.
	if got.CustomDomain {
		t.Error("CustomDomain leaked to grandfathered Free project")
	}
	if got.BYOSMTP {
		t.Error("BYOSMTP leaked to grandfathered Free project")
	}
	if got.QuotaAlerts {
		t.Error("QuotaAlerts leaked to grandfathered Free project")
	}
}

func TestLegacyFreeLimits_DoesNotMutateInput(t *testing.T) {
	// legacyFreeLimits takes a *PlanLimits and returns a *PlanLimits;
	// the returned value must be a copy, not a mutation of the input.
	// Otherwise the LimitsService cache (which hands out shared
	// pointers) would get corrupted the first time a grandfathered
	// project runs an enforcement check.
	current := &PlanLimits{
		Plan:          "free",
		MAULimit:      5000,
		StorageMB:     512,
		BandwidthMB:   2048,
		WSConnections: 50,
	}
	_ = legacyFreeLimits(current)
	if current.MAULimit != 5000 || current.StorageMB != 512 ||
		current.BandwidthMB != 2048 || current.WSConnections != 50 {
		t.Fatal("legacyFreeLimits mutated its input — cache corruption risk")
	}
}
