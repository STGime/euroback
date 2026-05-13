package query

import (
	"testing"
)

func TestDetectDDL(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want []DetectedDDL
	}{
		// ── No DDL ──
		{
			name: "select",
			sql:  `SELECT * FROM users WHERE id = 1`,
			want: nil,
		},
		{
			name: "insert",
			sql:  `INSERT INTO sessions (id, user_id) VALUES (gen_random_uuid(), 1)`,
			want: nil,
		},

		// ── CREATE TABLE ──
		{
			name: "create_table_simple",
			sql:  `CREATE TABLE app_users (id uuid primary key)`,
			want: []DetectedDDL{{Action: "create_table", TableName: "app_users", Detail: map[string]any{"source": "sql"}}},
		},
		{
			name: "create_table_if_not_exists",
			sql:  `CREATE TABLE IF NOT EXISTS app_users (id uuid)`,
			want: []DetectedDDL{{Action: "create_table", TableName: "app_users", Detail: map[string]any{"source": "sql"}}},
		},
		{
			name: "create_table_schema_qualified",
			sql:  `CREATE TABLE tenant_abc.app_users (id uuid)`,
			want: []DetectedDDL{{Action: "create_table", TableName: "app_users", Detail: map[string]any{"source": "sql"}}},
		},
		{
			name: "create_table_quoted_name",
			sql:  `CREATE TABLE "Mixed Case" (id uuid)`,
			want: []DetectedDDL{{Action: "create_table", TableName: "Mixed Case", Detail: map[string]any{"source": "sql"}}},
		},
		{
			name: "create_temp_table",
			sql:  `CREATE TEMP TABLE staging (id uuid)`,
			want: []DetectedDDL{{Action: "create_table", TableName: "staging", Detail: map[string]any{"source": "sql"}}},
		},

		// ── DROP TABLE ──
		{
			name: "drop_table",
			sql:  `DROP TABLE app_users`,
			want: []DetectedDDL{{Action: "drop_table", TableName: "app_users", Detail: map[string]any{"source": "sql"}}},
		},
		{
			name: "drop_table_if_exists",
			sql:  `DROP TABLE IF EXISTS app_users CASCADE`,
			want: []DetectedDDL{{Action: "drop_table", TableName: "app_users", Detail: map[string]any{"source": "sql"}}},
		},

		// ── ALTER TABLE ADD COLUMN — the bug that prompted #119/#120 ──
		{
			name: "add_column",
			sql:  `ALTER TABLE app_users ADD COLUMN stable_device_id text`,
			want: []DetectedDDL{{
				Action: "add_column", TableName: "app_users",
				ColumnName: ptrTo("stable_device_id"),
				Detail:     map[string]any{"source": "sql", "type": "text"},
			}},
		},
		{
			name: "add_column_if_not_exists",
			sql:  `ALTER TABLE app_users ADD COLUMN IF NOT EXISTS stable_device_id text`,
			want: []DetectedDDL{{
				Action: "add_column", TableName: "app_users",
				ColumnName: ptrTo("stable_device_id"),
				Detail:     map[string]any{"source": "sql", "type": "text"},
			}},
		},

		// ── ALTER TABLE DROP COLUMN ──
		{
			name: "drop_column",
			sql:  `ALTER TABLE app_users DROP COLUMN stable_device_id`,
			want: []DetectedDDL{{
				Action: "drop_column", TableName: "app_users",
				ColumnName: ptrTo("stable_device_id"),
				Detail:     map[string]any{"source": "sql"},
			}},
		},

		// ── ALTER TABLE RENAME ──
		{
			name: "rename_table",
			sql:  `ALTER TABLE app_users RENAME TO app_users_v2`,
			want: []DetectedDDL{{
				Action: "rename_table", TableName: "app_users_v2",
				Detail: map[string]any{"source": "sql", "old_name": "app_users"},
			}},
		},
		{
			name: "rename_column",
			sql:  `ALTER TABLE app_users RENAME COLUMN old_id TO new_id`,
			want: []DetectedDDL{{
				Action: "alter_column", TableName: "app_users",
				ColumnName: ptrTo("old_id"),
				Detail:     map[string]any{"source": "sql", "rename_to": "new_id", "kind": "rename_column"},
			}},
		},

		// ── ALTER COLUMN ──
		{
			name: "alter_column_type",
			sql:  `ALTER TABLE app_users ALTER COLUMN email TYPE varchar`,
			want: []DetectedDDL{{
				Action: "alter_column", TableName: "app_users",
				ColumnName: ptrTo("email"),
				Detail:     map[string]any{"source": "sql", "kind": "type", "new_type": "varchar"},
			}},
		},
		{
			name: "alter_column_set_not_null",
			sql:  `ALTER TABLE app_users ALTER COLUMN email SET NOT NULL`,
			want: []DetectedDDL{{
				Action: "alter_column", TableName: "app_users",
				ColumnName: ptrTo("email"),
				Detail:     map[string]any{"source": "sql", "kind": "set_not_null"},
			}},
		},

		// ── CREATE / DROP INDEX ──
		{
			name: "create_index",
			sql:  `CREATE UNIQUE INDEX app_users_stable_uniq ON app_users(stable_device_id) WHERE stable_device_id IS NOT NULL`,
			want: []DetectedDDL{{
				Action: "create_index", TableName: "app_users",
				Detail: map[string]any{"source": "sql", "index_name": "app_users_stable_uniq"},
			}},
		},
		{
			name: "drop_index",
			sql:  `DROP INDEX IF EXISTS app_users_stable_uniq`,
			want: []DetectedDDL{{
				Action: "drop_index", TableName: "app_users_stable_uniq",
				Detail: map[string]any{
					"source":     "sql",
					"index_name": "app_users_stable_uniq",
					"note":       "table_name contains the index name; target table is not resolvable from DROP INDEX alone",
				},
			}},
		},

		// ── False-positive guards ──
		{
			name: "string_literal_containing_ddl",
			sql:  `SELECT 'CREATE TABLE fake_table (id int)' AS msg`,
			want: nil,
		},
		{
			name: "comment_containing_ddl",
			sql:  `SELECT 1 -- CREATE TABLE fake_table (id int)`,
			want: nil,
		},
		{
			name: "block_comment_containing_ddl",
			sql:  `/* DROP TABLE fake_table */ SELECT 1`,
			want: nil,
		},
		{
			name: "dollar_quoted_function_body",
			sql:  `CREATE FUNCTION refresh_v() RETURNS void LANGUAGE plpgsql AS $$ BEGIN DROP TABLE not_really; END $$`,
			want: nil, // CREATE FUNCTION is intentionally not detected; the DROP inside the body is stripped
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DetectDDL(tc.sql)
			if !equalDetected(got, tc.want) {
				t.Errorf("DetectDDL(%q):\n  got  %+v\n  want %+v", tc.sql, got, tc.want)
			}
		})
	}
}

func ptrTo(s string) *string { return &s }

// equalDetected compares two []DetectedDDL slices, treating Detail as a
// shallow map comparison. The order matters — detectors fire in
// declaration order — but for this test the inputs each match at most
// one detector so ordering equality is trivially satisfied.
func equalDetected(a, b []DetectedDDL) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Action != b[i].Action || a[i].TableName != b[i].TableName {
			return false
		}
		if (a[i].ColumnName == nil) != (b[i].ColumnName == nil) {
			return false
		}
		if a[i].ColumnName != nil && *a[i].ColumnName != *b[i].ColumnName {
			return false
		}
		if !mapEqual(a[i].Detail, b[i].Detail) {
			return false
		}
	}
	return true
}

func mapEqual(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok {
			return false
		}
		if !valEqual(va, vb) {
			return false
		}
	}
	return true
}

func valEqual(a, b any) bool {
	switch av := a.(type) {
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	default:
		return a == b
	}
}
