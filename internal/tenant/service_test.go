package tenant

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// setupTestDB creates a connection pool to the local test database, runs
// migrations if necessary, and returns the pool plus a cleanup function.
func setupTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgres://eurobase_api:localdev@localhost:5433/eurobase?sslmode=disable"
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Skipf("cannot connect to test database: %v", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("cannot ping test database: %v", err)
	}

	// Verify that the platform_users table exists (migrations ran).
	var tableExists bool
	err = pool.QueryRow(ctx,
		`SELECT EXISTS (
			SELECT FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = 'platform_users'
		)`,
	).Scan(&tableExists)
	if err != nil || !tableExists {
		pool.Close()
		t.Skip("migrations not applied; run setup-local.sh first")
	}

	t.Cleanup(func() {
		pool.Close()
	})

	return pool
}

// cleanupProject removes a project and its tenant schema from the database.
func cleanupProject(t *testing.T, pool *pgxpool.Pool, projectID string) {
	t.Helper()
	ctx := context.Background()

	// Deprovision the tenant schema.
	_, err := pool.Exec(ctx, `SELECT deprovision_tenant($1)`, projectID)
	if err != nil {
		slog.Warn("deprovision_tenant cleanup failed", "project_id", projectID, "error", err)
	}

	// Delete the project row (cascades to api_keys).
	_, err = pool.Exec(ctx, `DELETE FROM projects WHERE id = $1`, projectID)
	if err != nil {
		slog.Warn("delete project cleanup failed", "project_id", projectID, "error", err)
	}

	// Clean up the platform user created for this test (best-effort).
	_, _ = pool.Exec(ctx, `DELETE FROM platform_users WHERE hanko_user_id LIKE 'test-%'`)
}

func TestCreateProject(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	// Create service without a river client (nil — skip async jobs in tests).
	svc := &TenantService{pool: pool}

	req := CreateProjectRequest{
		Name:   "Test Project",
		Slug:   "test-create-proj",
		Region: "fr-par",
		Plan:   "free",
	}

	project, err := svc.CreateProject(ctx, "test-user-create", "test@eurobase.app", req)
	if err != nil {
		t.Fatalf("CreateProject() returned error: %v", err)
	}

	t.Cleanup(func() {
		cleanupProject(t, pool, project.ID)
	})

	// Assert returned fields.
	if project.Name != req.Name {
		t.Errorf("expected name %q, got %q", req.Name, project.Name)
	}
	if project.Slug != req.Slug {
		t.Errorf("expected slug %q, got %q", req.Slug, project.Slug)
	}
	if project.Region != req.Region {
		t.Errorf("expected region %q, got %q", req.Region, project.Region)
	}
	if project.Plan != req.Plan {
		t.Errorf("expected plan %q, got %q", req.Plan, project.Plan)
	}
	if project.Status != "provisioning" {
		t.Errorf("expected status 'provisioning', got %q", project.Status)
	}
	if project.ID == "" {
		t.Error("expected non-empty project ID")
	}
	if project.OwnerID == "" {
		t.Error("expected non-empty owner ID")
	}

	// Verify the tenant schema was created by querying pg_namespace.
	var schemaExists bool
	err = pool.QueryRow(ctx,
		`SELECT EXISTS (
			SELECT FROM pg_namespace WHERE nspname = $1
		)`,
		project.SchemaName,
	).Scan(&schemaExists)
	if err != nil {
		t.Fatalf("failed to check schema existence: %v", err)
	}
	if !schemaExists {
		t.Errorf("tenant schema %q was not created", project.SchemaName)
	}
}

func TestCreateProjectDuplicateSlug(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	svc := &TenantService{pool: pool}

	req := CreateProjectRequest{
		Name:   "Duplicate Test",
		Slug:   "test-dup-slug",
		Region: "fr-par",
		Plan:   "free",
	}

	project1, err := svc.CreateProject(ctx, "test-user-dup1", "dup1@eurobase.app", req)
	if err != nil {
		t.Fatalf("first CreateProject() returned error: %v", err)
	}

	t.Cleanup(func() {
		cleanupProject(t, pool, project1.ID)
	})

	// Second creation with the same slug should fail.
	_, err = svc.CreateProject(ctx, "test-user-dup2", "dup2@eurobase.app", req)
	if err == nil {
		t.Fatal("expected error for duplicate slug, got nil")
	}

	// Clean up any orphaned user from the second attempt.
	_, _ = pool.Exec(ctx, `DELETE FROM platform_users WHERE hanko_user_id = $1`, "test-user-dup2")
}

func TestListProjects(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	svc := &TenantService{pool: pool}
	hankoUserID := "test-user-list"

	req1 := CreateProjectRequest{
		Name:   "List Test One",
		Slug:   "test-list-one",
		Region: "fr-par",
		Plan:   "free",
	}
	req2 := CreateProjectRequest{
		Name:   "List Test Two",
		Slug:   "test-list-two",
		Region: "fr-par",
		Plan:   "free",
	}

	p1, err := svc.CreateProject(ctx, hankoUserID, "list@eurobase.app", req1)
	if err != nil {
		t.Fatalf("CreateProject(1) returned error: %v", err)
	}
	p2, err := svc.CreateProject(ctx, hankoUserID, "list@eurobase.app", req2)
	if err != nil {
		t.Fatalf("CreateProject(2) returned error: %v", err)
	}

	t.Cleanup(func() {
		cleanupProject(t, pool, p2.ID)
		cleanupProject(t, pool, p1.ID)
	})

	projects, err := svc.ListProjects(ctx, hankoUserID)
	if err != nil {
		t.Fatalf("ListProjects() returned error: %v", err)
	}

	if len(projects) < 2 {
		t.Fatalf("expected at least 2 projects, got %d", len(projects))
	}

	// Verify descending created_at order (most recent first).
	if projects[0].CreatedAt.Before(projects[1].CreatedAt) {
		t.Error("expected projects in descending created_at order")
	}

	// Verify our projects are in the list.
	slugs := make(map[string]bool)
	for _, p := range projects {
		slugs[p.Slug] = true
	}
	if !slugs["test-list-one"] || !slugs["test-list-two"] {
		t.Error("expected both test projects in the list")
	}
}
