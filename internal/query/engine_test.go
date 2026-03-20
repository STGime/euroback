package query

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// setupTestDB connects to the local test database, creates a test tenant
// project, provisions its schema, sets the project to active, and returns
// the pool, schema name, project ID, and a cleanup function.
func setupTestDB(t *testing.T) (*pgxpool.Pool, string, string) {
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

	// Check that required tables/functions exist.
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

	// Create a test platform user.
	hankoUserID := fmt.Sprintf("test-query-engine-%d", os.Getpid())
	var ownerID string
	err = pool.QueryRow(ctx,
		`INSERT INTO platform_users (hanko_user_id, email)
		 VALUES ($1, $2)
		 ON CONFLICT (hanko_user_id) DO UPDATE SET email = EXCLUDED.email
		 RETURNING id`,
		hankoUserID, "querytest@eurobase.app",
	).Scan(&ownerID)
	if err != nil {
		pool.Close()
		t.Skipf("cannot create test user: %v", err)
	}

	// Create a test project.
	slug := fmt.Sprintf("test-query-%d", os.Getpid())
	schemaPlaceholder := fmt.Sprintf("tenant_test_query_%d", os.Getpid())
	s3Placeholder := fmt.Sprintf("eurobase-test-query-%d", os.Getpid())
	var projectID string
	err = pool.QueryRow(ctx,
		`INSERT INTO projects (owner_id, name, slug, schema_name, s3_bucket, region, plan, status)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, 'provisioning')
		 RETURNING id`,
		ownerID, "Query Engine Test", slug, schemaPlaceholder, s3Placeholder, "fr-par", "free",
	).Scan(&projectID)
	if err != nil {
		pool.Close()
		t.Skipf("cannot create test project: %v", err)
	}

	// Provision the tenant schema.
	_, err = pool.Exec(ctx, `SELECT provision_tenant($1, $2, $3)`, projectID, "Query Engine Test", "free")
	if err != nil {
		// Clean up on failure.
		_, _ = pool.Exec(ctx, `DELETE FROM projects WHERE id = $1`, projectID)
		_, _ = pool.Exec(ctx, `DELETE FROM platform_users WHERE hanko_user_id = $1`, hankoUserID)
		pool.Close()
		t.Skipf("cannot provision tenant: %v", err)
	}

	// Set project to active.
	_, err = pool.Exec(ctx, `UPDATE projects SET status = 'active' WHERE id = $1`, projectID)
	if err != nil {
		_, _ = pool.Exec(ctx, `SELECT deprovision_tenant($1)`, projectID)
		_, _ = pool.Exec(ctx, `DELETE FROM projects WHERE id = $1`, projectID)
		_, _ = pool.Exec(ctx, `DELETE FROM platform_users WHERE hanko_user_id = $1`, hankoUserID)
		pool.Close()
		t.Skipf("cannot activate project: %v", err)
	}

	// Read back the schema name.
	var schemaName string
	err = pool.QueryRow(ctx, `SELECT schema_name FROM projects WHERE id = $1`, projectID).Scan(&schemaName)
	if err != nil {
		pool.Close()
		t.Skipf("cannot read schema name: %v", err)
	}

	t.Cleanup(func() {
		ctx := context.Background()
		_, err := pool.Exec(ctx, `SELECT deprovision_tenant($1)`, projectID)
		if err != nil {
			slog.Warn("deprovision_tenant cleanup failed", "project_id", projectID, "error", err)
		}
		_, _ = pool.Exec(ctx, `DELETE FROM projects WHERE id = $1`, projectID)
		_, _ = pool.Exec(ctx, `DELETE FROM platform_users WHERE hanko_user_id = $1`, hankoUserID)
		pool.Close()
	})

	return pool, schemaName, projectID
}

// uuidToString converts a pgx UUID value (which may be [16]byte or pgtype.UUID)
// to its standard string representation.
func uuidToString(v interface{}) string {
	switch id := v.(type) {
	case [16]byte:
		return fmt.Sprintf("%x-%x-%x-%x-%x", id[0:4], id[4:6], id[6:8], id[8:10], id[10:16])
	case string:
		return id
	default:
		return fmt.Sprintf("%v", id)
	}
}

func TestInsertAndSelect(t *testing.T) {
	pool, schema, _ := setupTestDB(t)
	ctx := context.Background()
	engine := NewQueryEngine(pool)

	// Insert a row into the tenant's users table.
	data := map[string]interface{}{
		"email":        "alice@example.com",
		"display_name": "Alice",
	}
	row, err := engine.InsertRow(ctx, schema, "users", data)
	if err != nil {
		t.Fatalf("InsertRow failed: %v", err)
	}

	if row["email"] != "alice@example.com" {
		t.Errorf("expected email 'alice@example.com', got %v", row["email"])
	}
	if row["display_name"] != "Alice" {
		t.Errorf("expected display_name 'Alice', got %v", row["display_name"])
	}
	if row["id"] == nil || row["id"] == "" {
		t.Error("expected non-empty id in inserted row")
	}

	// Select it back.
	params := QueryParams{
		Filters: []Filter{{Column: "email", Operator: "eq", Value: "alice@example.com"}},
		Limit:   20,
		Offset:  0,
	}
	rows, count, err := engine.SelectRows(ctx, schema, "users", params)
	if err != nil {
		t.Fatalf("SelectRows failed: %v", err)
	}
	if count < 1 {
		t.Fatalf("expected count >= 1, got %d", count)
	}
	if len(rows) < 1 {
		t.Fatal("expected at least 1 row")
	}
	if rows[0]["email"] != "alice@example.com" {
		t.Errorf("expected email 'alice@example.com', got %v", rows[0]["email"])
	}
}

func TestSelectWithFilters(t *testing.T) {
	pool, schema, _ := setupTestDB(t)
	ctx := context.Background()
	engine := NewQueryEngine(pool)

	// Insert 3 rows with different display names.
	names := []string{"Alice", "Bob", "Alice"}
	for _, name := range names {
		_, err := engine.InsertRow(ctx, schema, "users", map[string]interface{}{
			"email":        fmt.Sprintf("%s@example.com", name),
			"display_name": name,
		})
		if err != nil {
			t.Fatalf("InsertRow(%s) failed: %v", name, err)
		}
	}

	// Select with eq filter on display_name=Alice.
	params := QueryParams{
		Filters: []Filter{{Column: "display_name", Operator: "eq", Value: "Alice"}},
		Limit:   20,
		Offset:  0,
	}
	rows, count, err := engine.SelectRows(ctx, schema, "users", params)
	if err != nil {
		t.Fatalf("SelectRows with filter failed: %v", err)
	}

	if count != 2 {
		t.Errorf("expected count=2 for 'Alice' filter, got %d", count)
	}
	if len(rows) != 2 {
		t.Errorf("expected 2 rows, got %d", len(rows))
	}
	for _, r := range rows {
		if r["display_name"] != "Alice" {
			t.Errorf("expected display_name 'Alice', got %v", r["display_name"])
		}
	}
}

func TestSelectWithOrder(t *testing.T) {
	pool, schema, _ := setupTestDB(t)
	ctx := context.Background()
	engine := NewQueryEngine(pool)

	// Insert rows.
	for _, name := range []string{"Zara", "Alice", "Middle"} {
		_, err := engine.InsertRow(ctx, schema, "users", map[string]interface{}{
			"display_name": name,
		})
		if err != nil {
			t.Fatalf("InsertRow(%s) failed: %v", name, err)
		}
	}

	// Select with order=created_at.desc.
	params := QueryParams{
		OrderBy: []OrderClause{{Column: "created_at", Descending: true}},
		Limit:   20,
		Offset:  0,
	}
	rows, _, err := engine.SelectRows(ctx, schema, "users", params)
	if err != nil {
		t.Fatalf("SelectRows with order failed: %v", err)
	}

	if len(rows) < 3 {
		t.Fatalf("expected at least 3 rows, got %d", len(rows))
	}

	// The last inserted row ("Middle") should appear first with DESC order.
	if rows[0]["display_name"] != "Middle" {
		t.Errorf("expected first row 'Middle' (most recent), got %v", rows[0]["display_name"])
	}
}

func TestSelectWithPagination(t *testing.T) {
	pool, schema, _ := setupTestDB(t)
	ctx := context.Background()
	engine := NewQueryEngine(pool)

	// Insert 5 rows.
	for i := 0; i < 5; i++ {
		_, err := engine.InsertRow(ctx, schema, "users", map[string]interface{}{
			"display_name": fmt.Sprintf("User%d", i),
		})
		if err != nil {
			t.Fatalf("InsertRow(User%d) failed: %v", i, err)
		}
	}

	// Select with limit=2, offset=2.
	params := QueryParams{
		OrderBy: []OrderClause{{Column: "created_at", Descending: false}},
		Limit:   2,
		Offset:  2,
	}
	rows, count, err := engine.SelectRows(ctx, schema, "users", params)
	if err != nil {
		t.Fatalf("SelectRows with pagination failed: %v", err)
	}

	if count != 5 {
		t.Errorf("expected total count=5, got %d", count)
	}
	if len(rows) != 2 {
		t.Errorf("expected 2 rows with limit=2, got %d", len(rows))
	}
}

func TestUpdateRow(t *testing.T) {
	pool, schema, _ := setupTestDB(t)
	ctx := context.Background()
	engine := NewQueryEngine(pool)

	// Insert a row.
	inserted, err := engine.InsertRow(ctx, schema, "users", map[string]interface{}{
		"display_name": "BeforeUpdate",
	})
	if err != nil {
		t.Fatalf("InsertRow failed: %v", err)
	}
	rowID := uuidToString(inserted["id"])

	// Update display_name.
	updated, err := engine.UpdateRow(ctx, schema, "users", rowID, map[string]interface{}{
		"display_name": "AfterUpdate",
	})
	if err != nil {
		t.Fatalf("UpdateRow failed: %v", err)
	}
	if updated["display_name"] != "AfterUpdate" {
		t.Errorf("expected display_name 'AfterUpdate', got %v", updated["display_name"])
	}

	// Select back to confirm persistence.
	params := QueryParams{
		Filters: []Filter{{Column: "id", Operator: "eq", Value: rowID}},
		Limit:   1,
		Offset:  0,
	}
	rows, _, err := engine.SelectRows(ctx, schema, "users", params)
	if err != nil {
		t.Fatalf("SelectRows after update failed: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0]["display_name"] != "AfterUpdate" {
		t.Errorf("expected display_name 'AfterUpdate' after re-select, got %v", rows[0]["display_name"])
	}
}

func TestDeleteRow(t *testing.T) {
	pool, schema, _ := setupTestDB(t)
	ctx := context.Background()
	engine := NewQueryEngine(pool)

	// Insert a row.
	inserted, err := engine.InsertRow(ctx, schema, "users", map[string]interface{}{
		"display_name": "ToBeDeleted",
	})
	if err != nil {
		t.Fatalf("InsertRow failed: %v", err)
	}
	rowID := uuidToString(inserted["id"])

	// Delete it.
	err = engine.DeleteRow(ctx, schema, "users", rowID)
	if err != nil {
		t.Fatalf("DeleteRow failed: %v", err)
	}

	// Select back: should be empty.
	params := QueryParams{
		Filters: []Filter{{Column: "id", Operator: "eq", Value: rowID}},
		Limit:   1,
		Offset:  0,
	}
	rows, count, err := engine.SelectRows(ctx, schema, "users", params)
	if err != nil {
		t.Fatalf("SelectRows after delete failed: %v", err)
	}
	if count != 0 {
		t.Errorf("expected count=0 after delete, got %d", count)
	}
	if len(rows) != 0 {
		t.Errorf("expected 0 rows after delete, got %d", len(rows))
	}
}

func TestValidateTable(t *testing.T) {
	pool, schema, _ := setupTestDB(t)
	ctx := context.Background()

	// Valid table should pass.
	err := ValidateTable(ctx, pool, schema, "users")
	if err != nil {
		t.Errorf("ValidateTable for existing table 'users' failed: %v", err)
	}

	// Nonexistent table should error.
	err = ValidateTable(ctx, pool, schema, "nonexistent_table_xyz")
	if err == nil {
		t.Error("expected error for nonexistent table, got nil")
	}
}

func TestValidateColumns(t *testing.T) {
	pool, schema, _ := setupTestDB(t)
	ctx := context.Background()

	// Valid columns should pass.
	err := ValidateColumns(ctx, pool, schema, "users", []string{"id", "email", "display_name"})
	if err != nil {
		t.Errorf("ValidateColumns for valid columns failed: %v", err)
	}

	// Invalid column should error.
	err = ValidateColumns(ctx, pool, schema, "users", []string{"id", "totally_fake_column"})
	if err == nil {
		t.Error("expected error for invalid column, got nil")
	}
}
