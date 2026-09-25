package cron

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Cron SQL is checked when it is saved (Create / Update / UpdateByName),
// not only when it first runs. Needs a migrated database:
//
//	DATABASE_URL=postgres://eurobase_api:localdev@localhost:5433/eurobase?sslmode=disable go test ./internal/cron/
func TestCronService_ValidatesSQLOnSave(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Skipf("connect: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("ping: %v", err)
	}

	var ownerID, projectID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO platform_users (email) VALUES ($1) RETURNING id`, "cron-validate-"+uuid.NewString()+"@test.eurobase.local",
	).Scan(&ownerID); err != nil {
		t.Fatalf("insert platform user: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO projects (owner_id, name, slug, schema_name, s3_bucket, region, plan, status)
		 VALUES ($1, 'cron validate', $2, $3, $2, 'fr-par', 'free', 'active')
		 RETURNING id`, ownerID, "cron-validate-"+uuid.NewString()[:8], "tenant_cron_validate_"+strings.ReplaceAll(uuid.NewString()[:8], "-", ""),
	).Scan(&projectID); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM cron_jobs WHERE project_id = $1`, projectID) //nolint:errcheck
		pool.Exec(ctx, `DELETE FROM projects WHERE id = $1`, projectID)          //nolint:errcheck
		pool.Exec(ctx, `DELETE FROM platform_users WHERE id = $1`, ownerID)      //nolint:errcheck
	})

	svc := NewCronService(pool)
	// A validation refusal, not an infrastructure error.
	isValidationErr := func(err error) bool {
		return err != nil && !strings.Contains(err.Error(), "could not validate")
	}
	foreign := `DELETE FROM "tenant_22222222_2222_2222_2222_222222222222".users`

	if _, err := svc.Create(ctx, projectID, CreateCronJobRequest{
		Name: "bad", Schedule: "0 * * * *", ActionType: "sql", Action: foreign,
	}); !isValidationErr(err) {
		t.Fatalf("Create with SQL referencing another tenant: err = %v, want a validation error", err)
	}

	job, err := svc.Create(ctx, projectID, CreateCronJobRequest{
		Name: "good", Schedule: "0 * * * *", ActionType: "sql", Action: "DELETE FROM events WHERE old",
	})
	if err != nil {
		t.Fatalf("Create rejected valid SQL: %v", err)
	}

	if _, err := svc.Update(ctx, projectID, job.ID, UpdateCronJobRequest{Action: &foreign}); !isValidationErr(err) {
		t.Fatalf("Update with SQL referencing another tenant: err = %v, want a validation error", err)
	}
	grant := "GRANT SELECT ON events TO PUBLIC"
	if _, err := svc.UpdateByName(ctx, projectID, "good", UpdateCronJobRequest{Action: &grant}); !isValidationErr(err) {
		t.Fatalf("UpdateByName with a GRANT: err = %v, want a validation error", err)
	}
	doBlock := "DO $$BEGIN NULL; END$$"
	if _, err := svc.Update(ctx, projectID, job.ID, UpdateCronJobRequest{Action: &doBlock}); !isValidationErr(err) {
		t.Fatalf("Update with a DO block: err = %v, want a validation error", err)
	}
	if _, err := svc.Create(ctx, projectID, CreateCronJobRequest{
		Name: "catalog-rpc", Schedule: "0 * * * *", ActionType: "rpc", Action: "pg_sleep",
	}); !isValidationErr(err) {
		t.Fatalf("Create rpc on a catalog function: err = %v, want a validation error", err)
	}
	missing := "00000000-0000-0000-0000-000000000000"
	if _, err := svc.Update(ctx, projectID, missing, UpdateCronJobRequest{Action: &foreign}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Update of a missing job: err = %v, want ErrNotFound", err)
	}

	// Switching an rpc job to sql validates its stored action too.
	rpc, err := svc.Create(ctx, projectID, CreateCronJobRequest{
		Name: "rpc", Schedule: "0 * * * *", ActionType: "rpc", Action: "rollup",
	})
	if err != nil {
		t.Fatalf("Create rpc: %v", err)
	}
	sqlType := "sql"
	if _, err := svc.Update(ctx, projectID, rpc.ID, UpdateCronJobRequest{ActionType: &sqlType, Action: &foreign}); !isValidationErr(err) {
		t.Fatalf("Update to sql with SQL referencing another tenant: err = %v, want a validation error", err)
	}
}

// run_as (#643): new jobs default to service; explicit values and updates
// are kept; anything else is refused.
func TestCronService_RunAs(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Skipf("connect: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("ping: %v", err)
	}
	var ownerID, projectID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO platform_users (email) VALUES ($1) RETURNING id`, "cron-runas-"+uuid.NewString()+"@test.eurobase.local",
	).Scan(&ownerID); err != nil {
		t.Fatalf("insert platform user: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO projects (owner_id, name, slug, schema_name, s3_bucket, region, plan, status)
		 VALUES ($1, 'cron runas', $2, $3, $2, 'fr-par', 'free', 'active')
		 RETURNING id`, ownerID, "cron-runas-"+uuid.NewString()[:8], "tenant_cron_runas_"+strings.ReplaceAll(uuid.NewString()[:8], "-", ""),
	).Scan(&projectID); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM cron_jobs WHERE project_id = $1`, projectID) //nolint:errcheck
		pool.Exec(ctx, `DELETE FROM projects WHERE id = $1`, projectID)          //nolint:errcheck
		pool.Exec(ctx, `DELETE FROM platform_users WHERE id = $1`, ownerID)      //nolint:errcheck
	})
	svc := NewCronService(pool)

	def, err := svc.Create(ctx, projectID, CreateCronJobRequest{
		Name: "default", Schedule: "0 * * * *", ActionType: "sql", Action: "DELETE FROM events WHERE old",
	})
	if err != nil {
		t.Fatal(err)
	}
	if def.RunAs != RunAsService {
		t.Errorf("new job run_as = %q, want %q", def.RunAs, RunAsService)
	}

	none := RunAsNone
	explicit, err := svc.Create(ctx, projectID, CreateCronJobRequest{
		Name: "explicit", Schedule: "0 * * * *", ActionType: "sql", Action: "DELETE FROM events WHERE old", RunAs: &none,
	})
	if err != nil {
		t.Fatal(err)
	}
	if explicit.RunAs != RunAsNone {
		t.Errorf("explicit run_as = %q, want %q", explicit.RunAs, RunAsNone)
	}

	service := RunAsService
	upd, err := svc.Update(ctx, projectID, explicit.ID, UpdateCronJobRequest{RunAs: &service})
	if err != nil {
		t.Fatal(err)
	}
	if upd.RunAs != RunAsService {
		t.Errorf("updated run_as = %q, want %q", upd.RunAs, RunAsService)
	}

	bad := "admin"
	if _, err := svc.Create(ctx, projectID, CreateCronJobRequest{
		Name: "bad", Schedule: "0 * * * *", ActionType: "sql", Action: "DELETE FROM events WHERE old", RunAs: &bad,
	}); err == nil {
		t.Error("Create accepted run_as=admin")
	}
	if _, err := svc.Update(ctx, projectID, def.ID, UpdateCronJobRequest{RunAs: &bad}); err == nil {
		t.Error("Update accepted run_as=admin")
	}
}
