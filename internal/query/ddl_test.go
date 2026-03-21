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
