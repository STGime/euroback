package tenant

import (
	"context"
	"testing"
)

// A Settings save sends the stored rate_limits back unchanged. Limits ops
// raised above the ceilings by hand must not make that save fail; a
// changed value above the ceiling is still refused.
func TestUpdateAuthConfig_UnchangedRateLimitsAboveCeiling(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()
	svc := &TenantService{pool: pool}

	uid := insertTestPlatformUser(t, pool, "ratelimits@test.eurobase.local")
	project, err := svc.CreateProject(ctx, uid, "ratelimits@test.eurobase.local", CreateProjectRequest{
		Name: "Rate Limits", Slug: "test-rate-limits-keep", Region: "fr-par", Plan: "free",
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Ops raised a limit above the ceiling by hand.
	if _, err := pool.Exec(ctx,
		`UPDATE projects SET auth_config = jsonb_set(coalesce(auth_config, '{}'::jsonb), '{rate_limits}', '{"signup_signin_per_5min_per_ip": 300}') WHERE id = $1`,
		project.ID); err != nil {
		t.Fatal(err)
	}

	cfg := AuthConfig{
		PasswordMinLength: 8,
		DisableSignup:     true,
		RateLimits:        &RateLimits{SignupSigninPer5MinPerIP: 300},
	}
	if _, err := svc.UpdateAuthConfig(ctx, project.ID, uid, cfg); err != nil {
		t.Fatalf("save with unchanged raised limits: %v", err)
	}

	cfg.RateLimits = &RateLimits{SignupSigninPer5MinPerIP: 301}
	if _, err := svc.UpdateAuthConfig(ctx, project.ID, uid, cfg); err == nil {
		t.Fatal("a changed value above the ceiling must be refused")
	}
}
