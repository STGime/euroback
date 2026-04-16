package query

import (
	"context"
	"testing"
)

func TestCreateAndDropTable(t *testing.T) {
	pool, schema, _ := setupTestDB(t)
	ctx := context.Background()

	cols := []ColumnDefinition{
		{Name: "id", Type: "uuid", Nullable: false, DefaultValue: "public.uuid_generate_v4()", IsPrimaryKey: true},
		{Name: "title", Type: "text", Nullable: false},
		{Name: "done", Type: "boolean", Nullable: false, DefaultValue: "false"},
		{Name: "created_at", Type: "timestamptz", Nullable: false, DefaultValue: "now()"},
	}

	err := CreateTable(ctx, pool, schema, "todos", cols)
	if err != nil {
		t.Fatalf("CreateTable failed: %v", err)
	}

	// Verify table exists.
	err = ValidateTable(ctx, pool, schema, "todos")
	if err != nil {
		t.Fatalf("table 'todos' should exist after creation: %v", err)
	}

	// Verify columns.
	err = ValidateColumns(ctx, pool, schema, "todos", []string{"id", "title", "done", "created_at"})
	if err != nil {
		t.Fatalf("columns should exist: %v", err)
	}

	// Drop it.
	err = DropTable(ctx, pool, schema, "todos")
	if err != nil {
		t.Fatalf("DropTable failed: %v", err)
	}

	// Verify table no longer exists.
	err = ValidateTable(ctx, pool, schema, "todos")
	if err == nil {
		t.Fatal("table 'todos' should not exist after drop")
	}
}

func TestAuditRLSFlagsTables(t *testing.T) {
	pool, schema, _ := setupTestDB(t)
	ctx := context.Background()

	// Table with RLS enabled + no policies → warning.
	if err := CreateTable(ctx, pool, schema, "audit_no_policy", []ColumnDefinition{
		{Name: "id", Type: "integer", IsPrimaryKey: true},
		{Name: "title", Type: "text"},
	}); err != nil {
		t.Fatalf("CreateTable failed: %v", err)
	}
	defer DropTable(ctx, pool, schema, "audit_no_policy") //nolint:errcheck

	// Table with RLS disabled → critical.
	if err := CreateTable(ctx, pool, schema, "audit_rls_off", []ColumnDefinition{
		{Name: "id", Type: "integer", IsPrimaryKey: true},
	}); err != nil {
		t.Fatalf("CreateTable failed: %v", err)
	}
	defer DropTable(ctx, pool, schema, "audit_rls_off") //nolint:errcheck
	if _, err := pool.Exec(ctx, `ALTER TABLE `+schema+`.audit_rls_off DISABLE ROW LEVEL SECURITY`); err != nil {
		t.Fatalf("disable rls failed: %v", err)
	}

	entries, err := AuditRLS(ctx, pool, schema)
	if err != nil {
		t.Fatalf("AuditRLS failed: %v", err)
	}

	byName := map[string]RLSAuditEntry{}
	for _, e := range entries {
		byName[e.TableName] = e
	}

	if e, ok := byName["audit_no_policy"]; !ok {
		t.Fatal("expected audit_no_policy in audit results")
	} else if e.Severity != "warning" {
		t.Errorf("audit_no_policy: got severity %q, want warning", e.Severity)
	}

	if e, ok := byName["audit_rls_off"]; !ok {
		t.Fatal("expected audit_rls_off in audit results")
	} else if e.Severity != "critical" {
		t.Errorf("audit_rls_off: got severity %q, want critical", e.Severity)
	}
}

func TestDetectOwnerColumn(t *testing.T) {
	cases := []struct {
		name string
		cols []ColumnDefinition
		want string
	}{
		{"user_id wins", []ColumnDefinition{{Name: "id"}, {Name: "user_id"}}, "user_id"},
		{"owner_id fallback", []ColumnDefinition{{Name: "id"}, {Name: "owner_id"}}, "owner_id"},
		{"created_by fallback", []ColumnDefinition{{Name: "id"}, {Name: "created_by"}}, "created_by"},
		{"none", []ColumnDefinition{{Name: "id"}, {Name: "title"}}, ""},
		{"case insensitive", []ColumnDefinition{{Name: "User_ID"}}, "user_id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := detectOwnerColumn(tc.cols); got != tc.want {
				t.Errorf("detectOwnerColumn = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCreateTableDuplicate(t *testing.T) {
	pool, schema, _ := setupTestDB(t)
	ctx := context.Background()

	cols := []ColumnDefinition{
		{Name: "id", Type: "integer", IsPrimaryKey: true},
	}

	err := CreateTable(ctx, pool, schema, "dup_test", cols)
	if err != nil {
		t.Fatalf("first CreateTable failed: %v", err)
	}
	defer DropTable(ctx, pool, schema, "dup_test") //nolint:errcheck

	err = CreateTable(ctx, pool, schema, "dup_test", cols)
	if err == nil {
		t.Fatal("expected error creating duplicate table")
	}
}

func TestDropSystemTable(t *testing.T) {
	pool, schema, _ := setupTestDB(t)
	ctx := context.Background()

	err := DropTable(ctx, pool, schema, "storage_objects")
	if err == nil {
		t.Fatal("expected error dropping system table")
	}
}

func TestAddAndDropColumn(t *testing.T) {
	pool, schema, _ := setupTestDB(t)
	ctx := context.Background()

	cols := []ColumnDefinition{
		{Name: "id", Type: "uuid", Nullable: false, DefaultValue: "public.uuid_generate_v4()", IsPrimaryKey: true},
	}
	err := CreateTable(ctx, pool, schema, "col_test", cols)
	if err != nil {
		t.Fatalf("CreateTable failed: %v", err)
	}
	defer DropTable(ctx, pool, schema, "col_test") //nolint:errcheck

	// Add a column.
	err = AddColumn(ctx, pool, schema, "col_test", ColumnDefinition{
		Name: "description", Type: "text", Nullable: true,
	})
	if err != nil {
		t.Fatalf("AddColumn failed: %v", err)
	}

	// Verify it exists.
	err = ValidateColumns(ctx, pool, schema, "col_test", []string{"description"})
	if err != nil {
		t.Fatalf("column 'description' should exist: %v", err)
	}

	// Drop it.
	err = DropColumn(ctx, pool, schema, "col_test", "description")
	if err != nil {
		t.Fatalf("DropColumn failed: %v", err)
	}

	// Verify it's gone.
	err = ValidateColumns(ctx, pool, schema, "col_test", []string{"description"})
	if err == nil {
		t.Fatal("column 'description' should not exist after drop")
	}
}

func TestValidateIdentifier(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid", "users", false},
		{"valid underscore", "my_table", false},
		{"valid with digits", "table1", false},
		{"empty", "", true},
		{"starts with digit", "1table", true},
		{"has spaces", "my table", true},
		{"has dash", "my-table", true},
		{"sql injection", "users; DROP TABLE", true},
		{"quotes", `users"`, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateIdentifier(tt.input, "table")
			if (err != nil) != tt.wantErr {
				t.Errorf("validateIdentifier(%q) error = %v, wantErr = %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestValidateColumnType(t *testing.T) {
	tests := []struct {
		colType string
		wantErr bool
	}{
		{"text", false},
		{"integer", false},
		{"JSONB", false},
		{"timestamptz", false},
		{"uuid", false},
		{"xml", true},
		{"hstore", true},
		{"DROP TABLE", true},
	}

	for _, tt := range tests {
		t.Run(tt.colType, func(t *testing.T) {
			err := validateColumnType(tt.colType)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateColumnType(%q) error = %v, wantErr = %v", tt.colType, err, tt.wantErr)
			}
		})
	}
}
