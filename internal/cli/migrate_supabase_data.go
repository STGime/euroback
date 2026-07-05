package cli

// #270 (part of #267): `eurobase import supabase data` — pull row
// data + auth users from a Supabase project and emit a SQL file the
// tenant can apply against their Eurobase project.
//
// Emission approach: the file contains multi-row INSERT statements
// for each public table plus the translated `auth.users` /
// `auth.identities` → Eurobase `users` / `user_identities`. The
// tenant applies it via `eurobase migrations up` — same path as
// `schema` (#269), so no new backend surface is needed.
//
// Streaming COPY-to-COPY is the target end-state (issue #270's own
// scoping) but requires a `/platform/projects/{id}/data/copy`
// endpoint that doesn't exist today. Multi-row INSERT is ~5–10×
// slower than COPY at ingest but is what runs on the existing
// tenant-migrations endpoint. Scoped for follow-up work.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/spf13/cobra"
)

func importSupabaseDataCmd() *cobra.Command {
	var (
		outputPath    string
		migrationsDir string
		migName       string
		skipAuth      bool
		skipRows      bool
		tableOnly     string
	)
	cmd := &cobra.Command{
		Use:   "data",
		Short: "Translate Supabase row + auth data into a Eurobase migration",
		Long: `Read row data from a Supabase project's public schema plus
auth.users / auth.identities, and write a migration file the tenant can
apply against their Eurobase project.

Env vars:
  SUPABASE_DB_URL   postgres:// URL of the Supabase project (required)

Emits multi-row INSERT statements grouped by table, in FK topological
order so parents insert before children. Auth users are translated
into Eurobase's ` + "`users`" + ` + ` + "`user_identities`" + ` tables — UUIDs are
preserved so existing RLS policies keep lining up post-cutover.

Not migrated (by design):
  - Refresh tokens — users re-authenticate on first request post-cutover.
  - Session state — reissued on next sign-in.

By default writes to ./migrations/<epoch>_data_from_supabase.sql — the
tenant runs "eurobase migrations up" to apply. Pass --output to change
the file path or --output - to write to stdout.

Flags:
  --skip-auth   emit only public-table rows, no auth translation
  --skip-rows   emit only auth translation, no public-table rows
  --table       migrate only this single table (retry a partial run)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			dbURL := os.Getenv("SUPABASE_DB_URL")
			if dbURL == "" {
				return fmt.Errorf("SUPABASE_DB_URL is required (postgres:// URL of the Supabase project)")
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Minute)
			defer cancel()

			conn, err := pgx.Connect(ctx, dbURL)
			if err != nil {
				return fmt.Errorf("connect to Supabase: %w", err)
			}
			defer conn.Close(ctx)

			var body strings.Builder
			body.Grow(64 * 1024)
			counts := map[string]int64{}

			// ── Row data ──
			if !skipRows {
				n, err := emitRowData(ctx, conn, &body, tableOnly, counts)
				if err != nil {
					return fmt.Errorf("emit row data: %w", err)
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "Row data: %d table(s), %d row(s) total\n", n, sumCounts(counts))
			}

			// ── Auth users + identities ──
			if !skipAuth {
				nUsers, nIdent, notes, err := emitAuthData(ctx, conn, &body)
				if err != nil {
					return fmt.Errorf("emit auth data: %w", err)
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "Auth: %d user(s), %d identity link(s)\n", nUsers, nIdent)
				for _, note := range notes {
					fmt.Fprintf(cmd.ErrOrStderr(), "  note: %s\n", note)
				}
			}

			header := fmt.Sprintf(
				"-- Auto-translated from Supabase by `eurobase import supabase data`.\n"+
					"-- Generated: %s UTC\n"+
					"-- Command to apply: eurobase migrations up\n"+
					"-- \n"+
					"-- NOTE: refresh tokens + session state are NOT migrated.\n"+
					"-- Users re-authenticate on their first request post-cutover.\n\n",
				time.Now().UTC().Format(time.RFC3339),
			)
			payload := header + body.String()

			if outputPath == "-" {
				_, err := cmd.OutOrStdout().Write([]byte(payload))
				return err
			}
			path := outputPath
			if path == "" {
				if err := os.MkdirAll(migrationsDir, 0o755); err != nil {
					return fmt.Errorf("create %s: %w", migrationsDir, err)
				}
				fname := fmt.Sprintf("%d_%s.sql", time.Now().Unix(), migName)
				path = filepath.Join(migrationsDir, fname)
			}
			if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
				return fmt.Errorf("write migration file: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Migration written to %s\n", path)
			fmt.Fprintln(cmd.OutOrStdout(), "Next: `eurobase migrations up` to apply.")
			return nil
		},
	}
	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "Output path (default ./migrations/<epoch>_data_from_supabase.sql; use '-' for stdout)")
	cmd.Flags().StringVar(&migrationsDir, "migrations-dir", "./migrations", "Directory to write the migration file")
	cmd.Flags().StringVar(&migName, "name", "data_from_supabase", "Migration file basename (before .sql)")
	cmd.Flags().BoolVar(&skipAuth, "skip-auth", false, "Skip auth.users + auth.identities translation")
	cmd.Flags().BoolVar(&skipRows, "skip-rows", false, "Skip public-schema row data (auth only)")
	cmd.Flags().StringVar(&tableOnly, "table", "", "Migrate a single public-schema table by name (retry a partial run)")
	return cmd
}

func sumCounts(m map[string]int64) int64 {
	var total int64
	for _, v := range m {
		total += v
	}
	return total
}

// listPublicTables returns every user-visible table in the public
// schema, ordered by name (topology sort happens later). System
// tables and views are excluded.
func listPublicTables(ctx context.Context, conn *pgx.Conn) ([]tableRef, error) {
	rows, err := conn.Query(ctx, `
		SELECT c.relname
		  FROM pg_class c
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = 'public'
		   AND c.relkind = 'r'                   -- ordinary tables only
		   AND c.relname NOT LIKE 'pg\_%'
		   AND c.relpersistence = 'p'            -- skip unlogged/temp
		 ORDER BY c.relname
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []tableRef
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, tableRef{name: name})
	}
	return out, rows.Err()
}

// listPublicFKEdges returns FK dependencies between public-schema
// tables (parent → child). Cross-schema references are skipped.
func listPublicFKEdges(ctx context.Context, conn *pgx.Conn) ([]fkEdge, error) {
	rows, err := conn.Query(ctx, `
		SELECT cl.relname   AS child,
		       cp.relname   AS parent
		  FROM pg_constraint con
		  JOIN pg_class cl ON cl.oid = con.conrelid
		  JOIN pg_class cp ON cp.oid = con.confrelid
		  JOIN pg_namespace ncl ON ncl.oid = cl.relnamespace
		  JOIN pg_namespace ncp ON ncp.oid = cp.relnamespace
		 WHERE con.contype = 'f'
		   AND ncl.nspname = 'public'
		   AND ncp.nspname = 'public'
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var edges []fkEdge
	for rows.Next() {
		var child, parent string
		if err := rows.Scan(&child, &parent); err != nil {
			return nil, err
		}
		edges = append(edges, fkEdge{from: child, to: parent})
	}
	return edges, rows.Err()
}

// listColumns returns the ordered list of user columns for `table`
// (skipping system + generated columns — we don't want to try to
// insert into a `xmin` or an `attgenerated='s'` column).
func listColumns(ctx context.Context, conn *pgx.Conn, table string) ([]string, error) {
	rows, err := conn.Query(ctx, `
		SELECT a.attname
		  FROM pg_attribute a
		  JOIN pg_class c ON c.oid = a.attrelid
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = 'public'
		   AND c.relname = $1
		   AND a.attnum > 0
		   AND NOT a.attisdropped
		   AND a.attgenerated = ''       -- exclude STORED generated columns
		 ORDER BY a.attnum
	`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		cols = append(cols, name)
	}
	return cols, rows.Err()
}

// emitRowData enumerates public tables, orders by FK topology, and
// writes multi-row INSERTs for each. Returns the count of tables
// visited. Per-table row counts land in `counts`.
func emitRowData(ctx context.Context, conn *pgx.Conn, dest *strings.Builder, tableOnly string, counts map[string]int64) (int, error) {
	tables, err := listPublicTables(ctx, conn)
	if err != nil {
		return 0, fmt.Errorf("list tables: %w", err)
	}
	edges, err := listPublicFKEdges(ctx, conn)
	if err != nil {
		return 0, fmt.Errorf("list FK edges: %w", err)
	}
	ordered := sortTablesByFK(tables, edges)

	visited := 0
	for _, t := range ordered {
		if tableOnly != "" && t.name != tableOnly {
			continue
		}
		cols, err := listColumns(ctx, conn, t.name)
		if err != nil {
			return visited, fmt.Errorf("list columns for %s: %w", t.name, err)
		}
		if len(cols) == 0 {
			continue
		}
		n, err := emitOneTable(ctx, conn, dest, t.name, cols)
		if err != nil {
			return visited, fmt.Errorf("emit rows for %s: %w", t.name, err)
		}
		counts[t.name] = n
		visited++
	}
	return visited, nil
}

// emitOneTable reads every row from `table` (all columns) and writes
// batched INSERTs into `dest`. Returns the row count.
func emitOneTable(ctx context.Context, conn *pgx.Conn, dest *strings.Builder, table string, columns []string) (int64, error) {
	quoted := make([]string, len(columns))
	for i, c := range columns {
		quoted[i] = quoteIdent(c)
	}
	q := fmt.Sprintf("SELECT %s FROM %s", strings.Join(quoted, ", "), quoteIdent(table))
	rows, err := conn.Query(ctx, q)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	fmt.Fprintf(dest, "-- Table: %s\n", table)

	var batch [][]interface{}
	var total int64
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return total, err
		}
		batch = append(batch, coerceRow(vals))
		if len(batch) >= dataBatchSize {
			emitInsertBatch(dest, table, columns, batch)
			batch = batch[:0]
		}
		total++
	}
	if err := rows.Err(); err != nil {
		return total, err
	}
	if len(batch) > 0 {
		emitInsertBatch(dest, table, columns, batch)
	}
	if total == 0 {
		fmt.Fprintf(dest, "-- (no rows)\n\n")
	}
	return total, nil
}

// coerceRow adapts pgx's `[]any` row representation into the shapes
// sqlLiteral knows how to format. pgx returns JSONB as []byte or
// map[string]interface{}; UUID as [16]byte or string; timestamptz as
// time.Time; and so on. We normalize the ones we care about.
func coerceRow(vals []interface{}) []interface{} {
	out := make([]interface{}, len(vals))
	for i, v := range vals {
		out[i] = coerceValue(v)
	}
	return out
}

func coerceValue(v interface{}) interface{} {
	switch t := v.(type) {
	case nil:
		return nil
	case [16]byte:
		// UUID as raw bytes → canonical string form. pgx v5 returns
		// pgtype.UUID which unwraps to [16]byte; canonical text is
		// what our Eurobase schema expects.
		return fmt.Sprintf("%x-%x-%x-%x-%x", t[0:4], t[4:6], t[6:8], t[8:10], t[10:16])
	case map[string]interface{}:
		// JSONB decoded to a map — re-encode canonically.
		b, err := json.Marshal(t)
		if err != nil {
			return jsonbLiteral("{}")
		}
		return jsonbLiteral(string(b))
	case []interface{}:
		// JSONB array or Postgres array — best-effort re-encode.
		b, err := json.Marshal(t)
		if err != nil {
			return jsonbLiteral("[]")
		}
		return jsonbLiteral(string(b))
	default:
		return v
	}
}

// emitAuthData reads Supabase's auth.users + auth.identities, translates
// each row, and writes the corresponding INSERTs. Returns user + identity
// counts and any translator notes.
func emitAuthData(ctx context.Context, conn *pgx.Conn, dest *strings.Builder) (int64, int64, []string, error) {
	fmt.Fprintf(dest, "-- Auth users (auth.users → users)\n")
	// Guard the two migration tables inside a single transaction so a
	// partial fail leaves neither users nor identities behind.
	fmt.Fprintf(dest, "BEGIN;\n\n")

	users, notes, err := readAuthUsers(ctx, conn)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("read auth.users: %w", err)
	}
	var userBatch [][]interface{}
	for _, u := range users {
		row, rowNotes := translateAuthUser(u)
		notes = append(notes, prefixNotes(u.ID, rowNotes)...)
		userBatch = append(userBatch, userRowAsValues(row))
		if len(userBatch) >= dataBatchSize {
			emitInsertBatch(dest, "users", authUserColumns, userBatch)
			userBatch = userBatch[:0]
		}
	}
	if len(userBatch) > 0 {
		emitInsertBatch(dest, "users", authUserColumns, userBatch)
	}

	fmt.Fprintf(dest, "-- Auth identities (auth.identities → user_identities)\n")
	identities, err := readAuthIdentities(ctx, conn)
	if err != nil {
		// auth.identities is a relatively recent Supabase table — a
		// truly ancient project may not have it. Treat "relation
		// missing" as "no rows" rather than a hard fail.
		var pgErr *pgconn.PgError
		if !errorsAs(err, &pgErr) || pgErr.Code != "42P01" {
			return int64(len(users)), 0, notes, fmt.Errorf("read auth.identities: %w", err)
		}
		notes = append(notes, "auth.identities not found on source — OAuth link rows skipped")
		identities = nil
	}
	var identBatch [][]interface{}
	for _, i := range identities {
		row := translateAuthIdentity(i)
		identBatch = append(identBatch, identityRowAsValues(row))
		if len(identBatch) >= dataBatchSize {
			emitInsertBatch(dest, "user_identities", authIdentityColumns, identBatch)
			identBatch = identBatch[:0]
		}
	}
	if len(identBatch) > 0 {
		emitInsertBatch(dest, "user_identities", authIdentityColumns, identBatch)
	}

	fmt.Fprintf(dest, "COMMIT;\n\n")
	return int64(len(users)), int64(len(identities)), notes, nil
}

func prefixNotes(id string, notes []string) []string {
	out := make([]string, len(notes))
	for i, n := range notes {
		out[i] = "user " + id + ": " + n
	}
	return out
}

// readAuthUsers projects the Supabase auth.users columns we care about.
// Only rows with at least a non-null email OR phone survive — a bare
// UUID with no login handle isn't recoverable in Eurobase.
func readAuthUsers(ctx context.Context, conn *pgx.Conn) ([]supabaseUser, []string, error) {
	rows, err := conn.Query(ctx, `
		SELECT id::text, email, encrypted_password,
		       email_confirmed_at, phone, phone_confirmed_at,
		       last_sign_in_at, banned_until,
		       COALESCE(raw_user_meta_data::text, '{}'),
		       COALESCE(raw_app_meta_data::text, '{}'),
		       created_at, updated_at
		  FROM auth.users
		 WHERE email IS NOT NULL OR phone IS NOT NULL
		 ORDER BY created_at
	`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var out []supabaseUser
	var notes []string
	for rows.Next() {
		var u supabaseUser
		if err := rows.Scan(
			&u.ID, &u.Email, &u.EncryptedPassword,
			&u.EmailConfirmedAt, &u.Phone, &u.PhoneConfirmedAt,
			&u.LastSignInAt, &u.BannedUntil,
			&u.RawUserMetaData, &u.RawAppMetaData,
			&u.CreatedAt, &u.UpdatedAt,
		); err != nil {
			return out, notes, err
		}
		out = append(out, u)
	}
	return out, notes, rows.Err()
}

// readAuthIdentities projects the Supabase auth.identities columns we
// map. Returns 42P01 (undefined_table) on projects that predate the
// identities table.
func readAuthIdentities(ctx context.Context, conn *pgx.Conn) ([]supabaseIdentity, error) {
	rows, err := conn.Query(ctx, `
		SELECT id::text, user_id::text, provider,
		       COALESCE(provider_id, ''),
		       COALESCE(identity_data::text, '{}'),
		       last_sign_in_at, created_at, updated_at
		  FROM auth.identities
		 ORDER BY user_id, created_at
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []supabaseIdentity
	for rows.Next() {
		var i supabaseIdentity
		if err := rows.Scan(
			&i.ID, &i.UserID, &i.Provider,
			&i.ProviderID, &i.IdentityData,
			&i.LastSignInAt, &i.CreatedAt, &i.UpdatedAt,
		); err != nil {
			return out, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// errorsAs is a tiny wrapper around errors.As kept local so we don't
// re-import errors here just for one call (this file already carries
// enough imports). Matches the existing asExit pattern that PR #274
// standardised on.
func errorsAs(err error, target interface{}) bool {
	pt, ok := target.(**pgconn.PgError)
	if !ok || pt == nil {
		return false
	}
	for cur := err; cur != nil; {
		if e, ok := cur.(*pgconn.PgError); ok {
			*pt = e
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := cur.(unwrapper)
		if !ok {
			return false
		}
		cur = u.Unwrap()
	}
	return false
}
