package query

import (
	"strings"
	"testing"
)

func TestBuildSelectBasic(t *testing.T) {
	params := QueryParams{
		Limit:  20,
		Offset: 0,
	}

	sql, args := buildSelectQuery("tenant_abc", "users", params)

	// Must use quoted identifiers.
	if !strings.Contains(sql, `"tenant_abc"."users"`) {
		t.Errorf("expected quoted schema.table, got: %s", sql)
	}
	if !strings.HasPrefix(sql, "SELECT * FROM") {
		t.Errorf("expected SELECT * FROM prefix, got: %s", sql)
	}
	// LIMIT and OFFSET should be parameterized.
	if !strings.Contains(sql, "LIMIT $1 OFFSET $2") {
		t.Errorf("expected LIMIT $1 OFFSET $2, got: %s", sql)
	}
	if len(args) != 2 {
		t.Fatalf("expected 2 args, got %d", len(args))
	}
	if args[0] != 20 {
		t.Errorf("expected limit arg=20, got %v", args[0])
	}
	if args[1] != 0 {
		t.Errorf("expected offset arg=0, got %v", args[1])
	}
}

func TestBuildSelectWithColumns(t *testing.T) {
	params := QueryParams{
		Select: []string{"id", "name", "email"},
		Limit:  20,
		Offset: 0,
	}

	sql, _ := buildSelectQuery("tenant_abc", "users", params)

	if !strings.Contains(sql, `"id", "name", "email"`) {
		t.Errorf("expected quoted column list, got: %s", sql)
	}
	if strings.Contains(sql, "SELECT *") {
		t.Error("expected specific columns, not SELECT *")
	}
}

func TestBuildSelectWithFilters(t *testing.T) {
	params := QueryParams{
		Filters: []Filter{
			{Column: "name", Operator: "eq", Value: "Stefan"},
			{Column: "age", Operator: "gt", Value: "25"},
		},
		Limit:  20,
		Offset: 0,
	}

	sql, args := buildSelectQuery("tenant_abc", "users", params)

	// WHERE clause must exist.
	if !strings.Contains(sql, "WHERE") {
		t.Errorf("expected WHERE clause, got: %s", sql)
	}
	// Must use parameterized placeholders, not raw values.
	if strings.Contains(sql, "Stefan") {
		t.Errorf("raw value 'Stefan' appears in SQL -- must be parameterized: %s", sql)
	}
	if strings.Contains(sql, "'25'") {
		t.Errorf("raw value '25' appears in SQL -- must be parameterized: %s", sql)
	}
	// Check parameterized placeholders.
	if !strings.Contains(sql, "$1") || !strings.Contains(sql, "$2") {
		t.Errorf("expected $1 and $2 placeholders in WHERE, got: %s", sql)
	}
	// First two args are the filter values; last two are limit and offset.
	if len(args) != 4 {
		t.Fatalf("expected 4 args (2 filters + limit + offset), got %d", len(args))
	}
	if args[0] != "Stefan" {
		t.Errorf("expected first arg 'Stefan', got %v", args[0])
	}
	if args[1] != "25" {
		t.Errorf("expected second arg '25', got %v", args[1])
	}
}

func TestBuildSelectWithOrder(t *testing.T) {
	params := QueryParams{
		OrderBy: []OrderClause{
			{Column: "created_at", Descending: true},
		},
		Limit:  20,
		Offset: 0,
	}

	sql, _ := buildSelectQuery("tenant_abc", "users", params)

	if !strings.Contains(sql, `ORDER BY "created_at" DESC`) {
		t.Errorf("expected ORDER BY clause with DESC, got: %s", sql)
	}
}

func TestBuildSelectWithOrderAsc(t *testing.T) {
	params := QueryParams{
		OrderBy: []OrderClause{
			{Column: "name", Descending: false},
		},
		Limit:  20,
		Offset: 0,
	}

	sql, _ := buildSelectQuery("tenant_abc", "users", params)

	if !strings.Contains(sql, `ORDER BY "name" ASC`) {
		t.Errorf("expected ORDER BY clause with ASC, got: %s", sql)
	}
}

func TestBuildInsertQuery(t *testing.T) {
	// Use a single-key map to get deterministic output.
	data := map[string]interface{}{
		"email": "test@example.com",
	}

	sql, args := buildInsertQuery("tenant_abc", "users", data)

	if !strings.Contains(sql, `INSERT INTO "tenant_abc"."users"`) {
		t.Errorf("expected INSERT INTO with quoted identifiers, got: %s", sql)
	}
	if !strings.Contains(sql, "RETURNING *") {
		t.Errorf("expected RETURNING *, got: %s", sql)
	}
	if !strings.Contains(sql, "$1") {
		t.Errorf("expected parameterized placeholder $1, got: %s", sql)
	}
	// Raw value must not appear in SQL.
	if strings.Contains(sql, "test@example.com") {
		t.Errorf("raw value appears in SQL -- must be parameterized: %s", sql)
	}
	if len(args) != 1 {
		t.Fatalf("expected 1 arg, got %d", len(args))
	}
	if args[0] != "test@example.com" {
		t.Errorf("expected arg 'test@example.com', got %v", args[0])
	}
}

func TestBuildUpdateQuery(t *testing.T) {
	// Use a single-key map for deterministic output.
	data := map[string]interface{}{
		"display_name": "Updated Name",
	}

	sql, args := buildUpdateQuery("tenant_abc", "users", "row-id-123", data)

	if !strings.Contains(sql, `UPDATE "tenant_abc"."users"`) {
		t.Errorf("expected UPDATE with quoted identifiers, got: %s", sql)
	}
	if !strings.Contains(sql, `WHERE "id" = $1`) {
		t.Errorf("expected WHERE id = $1, got: %s", sql)
	}
	if !strings.Contains(sql, "RETURNING *") {
		t.Errorf("expected RETURNING *, got: %s", sql)
	}
	// $1 is the row ID, $2 is the update value.
	if !strings.Contains(sql, "$2") {
		t.Errorf("expected $2 placeholder for SET clause, got: %s", sql)
	}
	// Raw values must not appear in SQL.
	if strings.Contains(sql, "Updated Name") {
		t.Errorf("raw value appears in SQL -- must be parameterized: %s", sql)
	}
	if strings.Contains(sql, "row-id-123") {
		t.Errorf("raw row ID appears in SQL -- must be parameterized: %s", sql)
	}
	if len(args) != 2 {
		t.Fatalf("expected 2 args, got %d", len(args))
	}
	if args[0] != "row-id-123" {
		t.Errorf("expected first arg 'row-id-123', got %v", args[0])
	}
	if args[1] != "Updated Name" {
		t.Errorf("expected second arg 'Updated Name', got %v", args[1])
	}
}

func TestBuildDeleteQuery(t *testing.T) {
	sql, args := buildDeleteQuery("tenant_abc", "users", "row-id-456")

	if !strings.Contains(sql, `DELETE FROM "tenant_abc"."users"`) {
		t.Errorf("expected DELETE FROM with quoted identifiers, got: %s", sql)
	}
	if !strings.Contains(sql, `WHERE "id" = $1`) {
		t.Errorf("expected WHERE id = $1, got: %s", sql)
	}
	// Raw row ID must not appear in SQL.
	if strings.Contains(sql, "row-id-456") {
		t.Errorf("raw row ID appears in SQL -- must be parameterized: %s", sql)
	}
	if len(args) != 1 {
		t.Fatalf("expected 1 arg, got %d", len(args))
	}
	if args[0] != "row-id-456" {
		t.Errorf("expected arg 'row-id-456', got %v", args[0])
	}
}

func TestBuildSelectWithInFilter(t *testing.T) {
	params := QueryParams{
		Filters: []Filter{
			{Column: "status", Operator: "in", Value: "active,pending"},
		},
		Limit:  20,
		Offset: 0,
	}

	sql, args := buildSelectQuery("tenant_abc", "users", params)

	if !strings.Contains(sql, "IN (") {
		t.Errorf("expected IN clause, got: %s", sql)
	}
	// Two IN values + limit + offset = 4 args.
	if len(args) != 4 {
		t.Fatalf("expected 4 args, got %d", len(args))
	}
	if args[0] != "active" {
		t.Errorf("expected first IN arg 'active', got %v", args[0])
	}
	if args[1] != "pending" {
		t.Errorf("expected second IN arg 'pending', got %v", args[1])
	}
}

func TestBuildCountQuery(t *testing.T) {
	params := QueryParams{
		Filters: []Filter{
			{Column: "name", Operator: "eq", Value: "Stefan"},
		},
		Limit:  20,
		Offset: 0,
	}

	sql, args := buildCountQuery("tenant_abc", "users", params)

	if !strings.Contains(sql, "SELECT count(*)") {
		t.Errorf("expected SELECT count(*), got: %s", sql)
	}
	if !strings.Contains(sql, "WHERE") {
		t.Errorf("expected WHERE clause, got: %s", sql)
	}
	if len(args) != 1 {
		t.Fatalf("expected 1 arg, got %d", len(args))
	}
}

func TestNoUserInputInSQL(t *testing.T) {
	// Comprehensive check: build queries with various user inputs and
	// ensure none of the raw values appear in the SQL string.
	userInputs := []string{
		"Robert'; DROP TABLE users;--",
		"<script>alert(1)</script>",
		"test@evil.com",
	}

	for _, input := range userInputs {
		data := map[string]interface{}{"email": input}

		insertSQL, _ := buildInsertQuery("s", "t", data)
		if strings.Contains(insertSQL, input) {
			t.Errorf("raw input %q found in INSERT SQL: %s", input, insertSQL)
		}

		updateSQL, _ := buildUpdateQuery("s", "t", "id", data)
		if strings.Contains(updateSQL, input) {
			t.Errorf("raw input %q found in UPDATE SQL: %s", input, updateSQL)
		}
	}
}
