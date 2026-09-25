package cron

import (
	"context"
	"os"
	"testing"

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
		`INSERT INTO platform_users (email) VALUES ('cron-validate@test.eurobase.local') RETURNING id`,
	).Scan(&ownerID); err != nil {
		t.Fatalf("insert platform user: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO projects (owner_id, name, slug, schema_name, s3_bucket, region, plan, status)
		 VALUES ($1, 'cron validate', 'cron-validate', 'tenant_cron_validate', 'b-cron-validate', 'fr-par', 'free', 'active')
		 RETURNING id`, ownerID,
	).Scan(&projectID); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM cron_jobs WHERE project_id = $1`, projectID) //nolint:errcheck
		pool.Exec(ctx, `DELETE FROM projects WHERE id = $1`, projectID)          //nolint:errcheck
		pool.Exec(ctx, `DELETE FROM platform_users WHERE id = $1`, ownerID)      //nolint:errcheck
	})

	svc := NewCronService(pool)
	foreign := `DELETE FROM "tenant_22222222_2222_2222_2222_222222222222".users`

	if _, err := svc.Create(ctx, projectID, CreateCronJobRequest{
		Name: "bad", Schedule: "0 * * * *", ActionType: "sql", Action: foreign,
	}); err == nil {
		t.Fatal("Create accepted SQL referencing another tenant")
	}

	job, err := svc.Create(ctx, projectID, CreateCronJobRequest{
		Name: "good", Schedule: "0 * * * *", ActionType: "sql", Action: "DELETE FROM events WHERE old",
	})
	if err != nil {
		t.Fatalf("Create rejected valid SQL: %v", err)
	}

	if _, err := svc.Update(ctx, projectID, job.ID, UpdateCronJobRequest{Action: &foreign}); err == nil {
		t.Fatal("Update accepted SQL referencing another tenant")
	}
	grant := "GRANT SELECT ON events TO PUBLIC"
	if _, err := svc.UpdateByName(ctx, projectID, "good", UpdateCronJobRequest{Action: &grant}); err == nil {
		t.Fatal("UpdateByName accepted a GRANT")
	}

	// Switching an rpc job to sql validates its stored action too.
	rpc, err := svc.Create(ctx, projectID, CreateCronJobRequest{
		Name: "rpc", Schedule: "0 * * * *", ActionType: "rpc", Action: "rollup",
	})
	if err != nil {
		t.Fatalf("Create rpc: %v", err)
	}
	sqlType := "sql"
	if _, err := svc.Update(ctx, projectID, rpc.ID, UpdateCronJobRequest{ActionType: &sqlType, Action: &foreign}); err == nil {
		t.Fatal("Update to sql accepted SQL referencing another tenant")
	}
}
