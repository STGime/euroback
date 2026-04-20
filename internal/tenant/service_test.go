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

	// Clean up test platform users by email suffix.
	_, _ = pool.Exec(ctx, `DELETE FROM platform_users WHERE email LIKE '%@test.eurobase.local'`)
}

// insertTestPlatformUser creates a platform_users row with the given email
// and returns its UUID. Pair with cleanupProject to remove it after the test.
func insertTestPlatformUser(t *testing.T, pool *pgxpool.Pool, email string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	err := pool.QueryRow(ctx,
		`INSERT INTO platform_users (email, password_hash, email_confirmed_at)
		 VALUES ($1, 'x', now())
		 ON CONFLICT (email) DO UPDATE SET email = EXCLUDED.email
		 RETURNING id::text`, email,
	).Scan(&id)
	if err != nil {
		t.Fatalf("insert test platform user: %v", err)
	}
	return id
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

	uid := insertTestPlatformUser(t, pool, "create@test.eurobase.local")
	project, err := svc.CreateProject(ctx, uid, "create@test.eurobase.local", req)
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

	uid1 := insertTestPlatformUser(t, pool, "dup1@test.eurobase.local")
	project1, err := svc.CreateProject(ctx, uid1, "dup1@test.eurobase.local", req)
	if err != nil {
		t.Fatalf("first CreateProject() returned error: %v", err)
	}

	t.Cleanup(func() {
		cleanupProject(t, pool, project1.ID)
	})

	// Second creation with the same slug should fail.
	uid2 := insertTestPlatformUser(t, pool, "dup2@test.eurobase.local")
	_, err = svc.CreateProject(ctx, uid2, "dup2@test.eurobase.local", req)
	if err == nil {
		t.Fatal("expected error for duplicate slug, got nil")
	}
}

func TestListProjects(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	svc := &TenantService{pool: pool}
	uid := insertTestPlatformUser(t, pool, "list@test.eurobase.local")

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

	p1, err := svc.CreateProject(ctx, uid, "list@test.eurobase.local", req1)
	if err != nil {
		t.Fatalf("CreateProject(1) returned error: %v", err)
	}
	p2, err := svc.CreateProject(ctx, uid, "list@test.eurobase.local", req2)
	if err != nil {
		t.Fatalf("CreateProject(2) returned error: %v", err)
	}

	t.Cleanup(func() {
		cleanupProject(t, pool, p2.ID)
		cleanupProject(t, pool, p1.ID)
	})

	projects, err := svc.ListProjects(ctx, uid)
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
