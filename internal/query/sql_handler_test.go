package query

import (
	"context"
	"testing"
)

func TestValidateSelectOnly(t *testing.T) {
	tests := []struct {
		name    string
		sql     string
		wantErr bool
	}{
		{"simple select", "SELECT * FROM users", false},
		{"select with where", "SELECT id, email FROM users WHERE id = 1", false},
		{"CTE query", "WITH active AS (SELECT * FROM users WHERE active) SELECT * FROM active", false},
		{"empty query", "", true},
		{"insert", "INSERT INTO users (email) VALUES ('a@b.com')", true},
		{"update", "UPDATE users SET email = 'x'", true},
		{"delete", "DELETE FROM users WHERE id = 1", true},
		{"drop table", "DROP TABLE users", true},
		{"alter table", "ALTER TABLE users ADD COLUMN foo text", true},
		{"create table", "CREATE TABLE evil (id int)", true},
		{"truncate", "TRUNCATE users", true},
		{"grant", "GRANT ALL ON users TO public", true},
		{"set", "SET role = 'admin'", true},
		{"copy", "COPY users TO '/tmp/dump'", true},
		{"semicolon chaining", "SELECT 1; DROP TABLE users", true},
		{"select into", "SELECT * INTO new_table FROM users", true},
		{"for update", "SELECT * FROM users FOR UPDATE", true},
		{"for share", "SELECT * FROM users FOR SHARE", true},
		{"comment hiding insert", "SELECT 1 -- \n INSERT INTO users VALUES (1)", true},
		{"block comment hiding", "SELECT /* evil */ 1 INSERT INTO users VALUES (1)", true},
		{"select with subquery", "SELECT * FROM (SELECT id FROM users) AS sub", false},
		{"select with join", "SELECT u.id FROM users u JOIN orders o ON u.id = o.user_id", false},
		{"aggregate query", "SELECT count(*) FROM users GROUP BY email HAVING count(*) > 1", false},
		// Ensure "updated_at" column name does not trigger false positive for UPDATE keyword
		{"column name updated_at", "SELECT updated_at FROM users", false},
		// Trailing semicolon is fine — users naturally end statements with one
		{"trailing semicolon", "SELECT * FROM users;", false},
		{"trailing semicolon with whitespace", "SELECT * FROM users ;  ", false},
		// But chaining is still blocked
		{"semicolon chaining with trailing", "SELECT 1; SELECT 2;", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSelectOnly(tt.sql)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSelectOnly(%q) error = %v, wantErr = %v", tt.sql, err, tt.wantErr)
			}
		})
	}
}

func TestExecuteSQL(t *testing.T) {
	pool, schema, _ := setupTestDB(t)
	ctx := context.Background()
	engine := NewQueryEngine(pool)

	// Insert test data.
	_, err := engine.InsertRow(ctx, schema, "users", map[string]interface{}{
		"email":        "sql-test@example.com",
		"display_name": "SQL Tester",
	})
	if err != nil {
		t.Fatalf("InsertRow failed: %v", err)
	}

	// Execute a simple SELECT.
	columns, rows, err := engine.ExecuteSQL(ctx, schema, "SELECT email, display_name FROM users WHERE email = 'sql-test@example.com'", 100)
	if err != nil {
		t.Fatalf("ExecuteSQL failed: %v", err)
	}

	if len(columns) != 2 {
		t.Errorf("expected 2 columns, got %d", len(columns))
	}
	if columns[0] != "email" || columns[1] != "display_name" {
		t.Errorf("unexpected columns: %v", columns)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0]["email"] != "sql-test@example.com" {
		t.Errorf("expected email 'sql-test@example.com', got %v", rows[0]["email"])
	}
}

func TestExecuteSQLRowLimit(t *testing.T) {
	pool, schema, _ := setupTestDB(t)
	ctx := context.Background()
	engine := NewQueryEngine(pool)

	// Insert 5 rows.
	for i := 0; i < 5; i++ {
		_, err := engine.InsertRow(ctx, schema, "users", map[string]interface{}{
			"display_name": "LimitTest",
		})
		if err != nil {
			t.Fatalf("InsertRow failed: %v", err)
		}
	}

	// Execute with maxRows=2.
	_, rows, err := engine.ExecuteSQL(ctx, schema, "SELECT * FROM users", 2)
	if err != nil {
		t.Fatalf("ExecuteSQL failed: %v", err)
	}
	if len(rows) > 2 {
		t.Errorf("expected at most 2 rows, got %d", len(rows))
	}
}

func TestExecuteSQLReadOnly(t *testing.T) {
	pool, _, _ := setupTestDB(t)
	_ = pool

	// Attempting to execute a write via the SQL endpoint should fail
	// because ValidateSelectOnly would catch it before ExecuteSQL.
	// But if someone bypasses validation, the read-only transaction should block it.
	// We test the validation layer here.
	err := ValidateSelectOnly("INSERT INTO users (email) VALUES ('hack@evil.com')")
	if err == nil {
		t.Error("expected validation to block INSERT")
	}
}
