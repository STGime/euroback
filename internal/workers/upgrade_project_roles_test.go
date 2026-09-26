package workers

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/eurobase/euroback/internal/jobs"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// #673: the upgrade orchestrator runs on the pool cmd/worker gives it, with
// the production roles — not a superuser. On eurobase_gateway every upgrade
// failed at its first query (000117 revokes project_upgrades) and couldn't
// record the failure; on eurobase_developer every step works.
//
// Needs a superuser URL to a database migrated by scripts/db/apply-migrations.sh:
//
//	UPGRADE_TEST_ADMIN_URL=postgres://postgres@localhost:5432/eurobase \
//	go test ./internal/workers/ -run UpgradeWorker
func TestUpgradeWorker_ProductionRoles(t *testing.T) {
	adminURL := os.Getenv("UPGRADE_TEST_ADMIN_URL")
	if adminURL == "" {
		t.Skip("UPGRADE_TEST_ADMIN_URL not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	developer := poolAsRole(t, adminURL, "eurobase_developer")
	gateway := poolAsRole(t, adminURL, "eurobase_gateway")

	oldDrain := upgradeMaintenanceDrain
	upgradeMaintenanceDrain = 10 * time.Millisecond
	defer func() { upgradeMaintenanceDrain = oldDrain }()

	// newUpgrade creates a Pro project with an upgrade row in state.
	newUpgrade := func(t *testing.T, state string) (projectID, upgradeID string) {
		t.Helper()
		suffix := randHex(t, 4)
		projectID = "7d0f1c2e-0000-4000-8000-" + randHex(t, 6)
		mustExec(t, admin, `INSERT INTO platform_users (id, email) VALUES ($1, $2)`, projectID, "upgrade-"+suffix+"@team.test")
		mustExec(t, admin, `INSERT INTO projects (id, owner_id, name, slug, schema_name, s3_bucket, region, plan, status)
			VALUES ($1, $1, 'upgrade', $2, $3, $4, 'fr-par', 'pro', 'active')`,
			projectID, "upgrade-"+suffix, "tenant_upgrade_"+suffix, "b-upgrade-"+suffix)
		if err := admin.QueryRow(ctx, `INSERT INTO project_upgrades (project_id, from_plan, to_plan, state)
			VALUES ($1, 'pro', 'team', $2) RETURNING id`, projectID, state).Scan(&upgradeID); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = admin.Exec(context.Background(), `DELETE FROM project_upgrades WHERE project_id = $1`, projectID)
			_, _ = admin.Exec(context.Background(), `DELETE FROM projects WHERE id = $1`, projectID)
		})
		return projectID, upgradeID
	}
	run := func(pool *pgxpool.Pool, upgradeID string) error {
		w := &UpgradeProjectWorker{Pool: pool} // no Provisioner: the provisioning step fails cleanly
		return w.Work(ctx, &river.Job[jobs.UpgradeProjectArgs]{
			JobRow: &rivertype.JobRow{Attempt: 1},
			Args:   jobs.UpgradeProjectArgs{UpgradeID: upgradeID},
		})
	}
	read := func(t *testing.T, projectID, upgradeID string) (state, errText, plan string, maintenance bool) {
		t.Helper()
		var e *string
		if err := admin.QueryRow(ctx, `SELECT u.state, u.error, p.plan, p.maintenance_mode
			FROM project_upgrades u JOIN projects p ON p.id = u.project_id WHERE u.id = $1`, upgradeID).
			Scan(&state, &e, &plan, &maintenance); err != nil {
			t.Fatal(err)
		}
		if e != nil {
			errText = *e
		}
		return state, errText, plan, maintenance
	}

	t.Run("preflight: refuses the gateway pool, accepts the developer pool", func(t *testing.T) {
		if err := (&UpgradeProjectWorker{Pool: gateway}).Preflight(ctx); err == nil {
			t.Error("preflight passed on eurobase_gateway")
		}
		if err := (&UpgradeProjectWorker{Pool: developer}).Preflight(ctx); err != nil {
			t.Errorf("preflight on eurobase_developer: %v", err)
		}
	})

	t.Run("gateway pool: fails at the first query and can't record it (the bug)", func(t *testing.T) {
		projectID, upgradeID := newUpgrade(t, "requested")
		err := run(gateway, upgradeID)
		if err == nil || !strings.Contains(err.Error(), "permission denied for table project_upgrades") {
			t.Errorf("err = %v, want 42501 on project_upgrades", err)
		}
		if state, _, _, _ := read(t, projectID, upgradeID); state != "requested" {
			t.Errorf("state = %q, want it stuck in requested", state)
		}
	})

	t.Run("developer pool: a failing step is recorded and maintenance cleared", func(t *testing.T) {
		projectID, upgradeID := newUpgrade(t, "requested")
		err := run(developer, upgradeID)
		if err == nil || !strings.Contains(err.Error(), "Provisioner not configured") {
			t.Fatalf("err = %v, want the provisioner failure", err)
		}
		state, errText, plan, maintenance := read(t, projectID, upgradeID)
		if state != "failed" || !strings.Contains(errText, "Provisioner not configured") || maintenance || plan != "pro" {
			t.Errorf("state=%q error=%q plan=%q maintenance=%v; want failed, recorded, pro, off", state, errText, plan, maintenance)
		}
	})

	t.Run("developer pool: copying → cutting_over → live flips the plan", func(t *testing.T) {
		projectID, upgradeID := newUpgrade(t, "copying")
		mustExec(t, admin, `UPDATE projects SET maintenance_mode = true WHERE id = $1`, projectID)
		if err := run(developer, upgradeID); err != nil {
			t.Fatalf("Work: %v", err)
		}
		state, _, plan, maintenance := read(t, projectID, upgradeID)
		if state != "live" || plan != "team" || maintenance {
			t.Errorf("state=%q plan=%q maintenance=%v; want live, team, off", state, plan, maintenance)
		}
	})
}
