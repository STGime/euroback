package email

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The name auth emails show: auth_config.app_name when set, else the
// project name — read on the gateway pool, which sends them. Same env as
// TestEmailLog_ProductionRoles.
func TestProjectDisplayName(t *testing.T) {
	adminURL, gwURL := os.Getenv("EMAIL_LOG_TEST_ADMIN_URL"), os.Getenv("EMAIL_LOG_TEST_GATEWAY_URL")
	if adminURL == "" || gwURL == "" {
		t.Skip("EMAIL_LOG_TEST_{ADMIN,GATEWAY}_URL not set")
	}
	ctx := context.Background()
	open := func(u string) *pgxpool.Pool {
		p, err := pgxpool.New(ctx, u)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.Close)
		return p
	}
	admin, gw := open(adminURL), open(gwURL)

	b := make([]byte, 6)
	_, _ = rand.Read(b)
	suffix := hex.EncodeToString(b)
	projectID := "5e1a0c9d-0000-4000-8001-" + suffix
	if _, err := admin.Exec(ctx, `INSERT INTO platform_users (id, email) VALUES ($1, $2)`, projectID, "appname-"+suffix+"@test.local"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO projects (id, owner_id, name, slug, schema_name, s3_bucket, region, plan, status)
		VALUES ($1, $1, 'stupid-correlations', $2, $3, $4, 'fr-par', 'free', 'active')`,
		projectID, "appname-"+suffix, "tenant_appname_"+suffix, "b-appname-"+suffix); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), `DELETE FROM projects WHERE id = $1`, projectID)
		_, _ = admin.Exec(context.Background(), `DELETE FROM platform_users WHERE id = $1`, projectID)
	})

	for _, tc := range []struct {
		name, authConfig, want string
	}{
		{"no app_name", `{}`, "stupid-correlations"},
		{"blank app_name", `{"app_name": "   "}`, "stupid-correlations"},
		{"app_name set", `{"app_name": "Stupid Correlations"}`, "Stupid Correlations"},
	} {
		if _, err := admin.Exec(ctx, `UPDATE projects SET auth_config = $2::jsonb WHERE id = $1`, projectID, tc.authConfig); err != nil {
			t.Fatal(err)
		}
		got, err := ProjectDisplayName(ctx, gw, projectID)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
