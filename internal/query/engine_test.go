package query

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
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

	// Create a test platform user. platform_users lost the hanko_user_id
	// column when Hanko was ripped out of the platform-auth path; email
	// is the natural unique identifier now. The unique index on email
	// is partial (WHERE email <> '') so it can't be used as an ON CONFLICT
	// target — clear any residue from a crashed prior run first, then
	// plain INSERT.
	testEmail := fmt.Sprintf("query-engine-test-%d@eurobase.app", os.Getpid())
	_, _ = pool.Exec(ctx, `DELETE FROM platform_users WHERE email = $1`, testEmail)
	var ownerID string
	err = pool.QueryRow(ctx,
		`INSERT INTO platform_users (email) VALUES ($1) RETURNING id`,
		testEmail,
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
		_, _ = pool.Exec(ctx, `DELETE FROM platform_users WHERE email = $1`, testEmail)
		pool.Close()
		t.Skipf("cannot provision tenant: %v", err)
	}

	// Set project to active.
	_, err = pool.Exec(ctx, `UPDATE projects SET status = 'active' WHERE id = $1`, projectID)
	if err != nil {
		_, _ = pool.Exec(ctx, `SELECT deprovision_tenant($1)`, projectID)
		_, _ = pool.Exec(ctx, `DELETE FROM projects WHERE id = $1`, projectID)
		_, _ = pool.Exec(ctx, `DELETE FROM platform_users WHERE email = $1`, testEmail)
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
		_, _ = pool.Exec(ctx, `DELETE FROM platform_users WHERE email = $1`, testEmail)
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
	key, err := engine.DeleteRow(ctx, schema, "users", rowID)
	if err != nil {
		t.Fatalf("DeleteRow failed: %v", err)
	}
	if key["id"] != rowID {
		t.Errorf("DeleteRow key id = %v, want %s", key["id"], rowID)
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

// TestDeleteReturnsOwner — deletes return the row's key and owner columns
// (and nothing else), so the realtime DELETE event can reach the owner (#995).
func TestDeleteReturnsOwner(t *testing.T) {
	pool, schema, _ := setupTestDB(t)
	ctx := context.Background()
	engine := NewQueryEngine(pool)

	if _, err := pool.Exec(ctx, fmt.Sprintf(
		`CREATE TABLE %s.rt_notes (id uuid PRIMARY KEY DEFAULT gen_random_uuid(), created_by text, user_id uuid, body text)`,
		quoteIdent(schema))); err != nil {
		t.Fatalf("create table: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), fmt.Sprintf(`DROP TABLE IF EXISTS %s.rt_notes`, quoteIdent(schema)))
	})

	owner := "11111111-1111-1111-1111-111111111111"
	insert := func(userID interface{}) string {
		t.Helper()
		row, err := engine.InsertRow(ctx, schema, "rt_notes", map[string]interface{}{"user_id": userID, "created_by": "creator", "body": "secret"})
		if err != nil {
			t.Fatalf("InsertRow: %v", err)
		}
		return uuidToString(row["id"])
	}

	id := insert(owner)
	key, err := engine.DeleteRow(ctx, schema, "rt_notes", id)
	if err != nil {
		t.Fatalf("DeleteRow: %v", err)
	}
	want := map[string]interface{}{"id": id, "user_id": owner, "created_by": "creator"}
	if fmt.Sprint(key) != fmt.Sprint(want) {
		t.Errorf("DeleteRow key = %v, want %v (no row data beyond id + owner columns)", key, want)
	}
	if _, err := engine.DeleteRow(ctx, schema, "rt_notes", id); err == nil || !strings.Contains(err.Error(), "row not found") {
		t.Errorf("second DeleteRow: err = %v, want row not found", err)
	}

	a, b := insert(owner), insert(nil)
	keys, err := engine.DeleteRows(ctx, schema, "rt_notes", []string{a, b, "22222222-2222-2222-2222-222222222222"})
	if err != nil {
		t.Fatalf("DeleteRows: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("DeleteRows returned %d keys, want 2: %v", len(keys), keys)
	}
	byID := map[interface{}]map[string]interface{}{}
	for _, k := range keys {
		byID[k["id"]] = k
	}
	if byID[a]["user_id"] != owner {
		t.Errorf("bulk key for owned row = %v, want user_id %s", byID[a], owner)
	}
	if _, ok := byID[b]["user_id"]; ok {
		t.Errorf("bulk key for row with NULL user_id should omit it: %v", byID[b])
	}
	if _, ok := byID[b]["body"]; ok {
		t.Errorf("bulk key carries row data: %v", byID[b])
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
