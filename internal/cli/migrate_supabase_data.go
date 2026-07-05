package cli

// #270 (part of #267): `eurobase import supabase data` — pull row
// data + auth users from a Supabase project and emit migration files
// the tenant applies against their Eurobase project.
//
// Emission approach: multi-row INSERT statements per table plus the
// translated `auth.users` / `auth.identities` → Eurobase `users` /
// `user_identities`. Each output file is a standalone migration —
// no BEGIN/COMMIT wrappers because the tenant-migrations endpoint
// wraps every migration in its own transaction (and rejects
// bare-BEGIN inside the body). Output is split across files at
// 450 KB each because that endpoint caps each migration body at
// 512 KB (`maxMigrationSQLBytes` in internal/query/tenant_migrations.go).
//
// Streaming COPY-to-COPY is the target end-state (issue #270's own
// scoping) but requires a `/platform/projects/{id}/data/copy`
// endpoint that doesn't exist today. Multi-row INSERT is ~5–10×
// slower than COPY at ingest but is what runs on the existing
// tenant-migrations channel. The streaming variant lands with the
// backend endpoint in a later PR.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/spf13/cobra"
)

// maxPartBytes is the size ceiling per emitted migration file. The
// tenant-migrations endpoint caps at 512 KiB — we leave 60 KiB
// headroom for the per-file header + any margin the validator wants.
const maxPartBytes = 450 * 1024

func importSupabaseDataCmd() *cobra.Command {
	var (
		migrationsDir string
		migName       string
		skipAuth      bool
		skipRows      bool
		tableOnly     string
	)
	cmd := &cobra.Command{
		Use:   "data",
		Short: "Translate Supabase row + auth data into Eurobase migration files",
		Long: `Read row data from a Supabase project's public schema plus
auth.users / auth.identities, and write migration files the tenant applies
against their Eurobase project.

Env vars:
  SUPABASE_DB_URL   postgres:// URL of the Supabase project (required)

Emits multi-row INSERT statements grouped by table, in FK topological
order so parents insert before children. Auth users translate into
Eurobase's ` + "`users`" + ` + ` + "`user_identities`" + ` tables — UUIDs are preserved
so existing RLS policies keep lining up post-cutover.

Output is split across as many migration files as needed: the tenant-
migrations endpoint caps each body at 512 KiB, so a project with more
than ~1,500 rows lands in multiple files. Each file is a self-contained
migration; ` + "`eurobase migrations up`" + ` applies them in version order.

Not migrated (by design):
  - Refresh tokens — users re-authenticate on first request post-cutover.
  - Session state — reissued on next sign-in.

Files land under --migrations-dir (default ./migrations), named
<epoch>_<name>_part_NNN.sql. Rerun-safe: files are timestamped and
cycle-detected FK errors surface loudly rather than silently
mis-ordering the inserts.

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

			if err := os.MkdirAll(migrationsDir, 0o755); err != nil {
				return fmt.Errorf("create %s: %w", migrationsDir, err)
			}

			writer := newSQLFileWriter(migrationsDir, migName, time.Now(), maxPartBytes)

			counts := map[string]int64{}
			// ── Row data ──
			if !skipRows {
				n, err := emitRowData(ctx, conn, writer, tableOnly, counts)
				if err != nil {
					return fmt.Errorf("emit row data: %w", err)
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "Row data: %d table(s), %d row(s) total\n", n, sumCounts(counts))
			}

			// ── Auth users + identities ──
			if !skipAuth {
				nUsers, nIdent, notes, err := emitAuthData(ctx, conn, writer)
				if err != nil {
					return fmt.Errorf("emit auth data: %w", err)
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "Auth: %d user(s), %d identity link(s)\n", nUsers, nIdent)
				for _, note := range notes {
					fmt.Fprintf(cmd.ErrOrStderr(), "  note: %s\n", note)
				}
			}

			paths, err := writer.Close()
			if err != nil {
				return fmt.Errorf("finalise output: %w", err)
			}
			if len(paths) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "Nothing to write — the source project has no data (or --skip flags removed everything).")
				return nil
			}
			for _, p := range paths {
				fmt.Fprintf(cmd.OutOrStdout(), "Migration written to %s\n", p)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Next: `eurobase migrations up` to apply.")
			return nil
		},
	}
	cmd.Flags().StringVar(&migrationsDir, "migrations-dir", "./migrations", "Directory to write the migration files")
	cmd.Flags().StringVar(&migName, "name", "data_from_supabase", "Migration file basename (before _part_NNN.sql)")
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

// ── multi-file writer ───────────────────────────────────────────────

// sqlFileWriter buffers emitted SQL and rotates to a new file when
// the current one approaches the endpoint's body cap. Each emitted
// unit is one atomic statement (never split across files) so a
// batch never lands half-way across.
type sqlFileWriter struct {
	dir      string
	baseName string
	baseTime time.Time
	maxBytes int
	partIdx  int
	buf      strings.Builder
	paths    []string
}

func newSQLFileWriter(dir, base string, t time.Time, maxBytes int) *sqlFileWriter {
	return &sqlFileWriter{
		dir:      dir,
		baseName: base,
		baseTime: t,
		maxBytes: maxBytes,
	}
}

// WriteStatement appends one atomic SQL block (an INSERT batch, a
// comment, whatever). If the current buffer + new block would cross
// maxBytes, flushes to disk first so `block` starts in a fresh file.
// A single block larger than maxBytes still writes as-is (loud fail
// at apply time, but not silently split).
func (w *sqlFileWriter) WriteStatement(block string) error {
	if w.buf.Len() > 0 && w.buf.Len()+len(block) > w.maxBytes {
		if err := w.flush(); err != nil {
			return err
		}
	}
	if w.buf.Len() == 0 {
		w.buf.WriteString(w.header())
	}
	w.buf.WriteString(block)
	return nil
}

// Close flushes any pending buffer and returns the paths of all
// written files.
func (w *sqlFileWriter) Close() ([]string, error) {
	if err := w.flush(); err != nil {
		return w.paths, err
	}
	return w.paths, nil
}

func (w *sqlFileWriter) flush() error {
	if w.buf.Len() == 0 {
		return nil
	}
	w.partIdx++
	// Each file gets a distinct version (epoch + partIdx-1) so
	// `eurobase migrations up` orders them naturally.
	version := w.baseTime.Unix() + int64(w.partIdx-1)
	fname := fmt.Sprintf("%d_%s_part_%03d.sql", version, w.baseName, w.partIdx)
	path := filepath.Join(w.dir, fname)
	if err := os.WriteFile(path, []byte(w.buf.String()), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	w.paths = append(w.paths, path)
	w.buf.Reset()
	return nil
}

func (w *sqlFileWriter) header() string {
	return fmt.Sprintf(
		"-- Auto-translated from Supabase by `eurobase import supabase data`.\n"+
			"-- Part %d, generated %s UTC.\n"+
			"-- Command to apply: eurobase migrations up\n"+
			"-- \n"+
			"-- NOTE: refresh tokens + session state are NOT migrated.\n"+
			"-- Users re-authenticate on their first request post-cutover.\n\n",
		w.partIdx+1, w.baseTime.UTC().Format(time.RFC3339),
	)
}

// ── column type introspection ───────────────────────────────────────

// columnInfo carries name + Postgres type name so emit can decide
// whether to cast the column to text in the SELECT (see #275 review
// #3/#4 — a JSONB scalar like `"foo"` decoded by pgx as a bare Go
// string produced an invalid INSERT because the value went to the
// jsonb column without a `::jsonb` cast).
type columnInfo struct {
	name   string
	pgType string // e.g. "jsonb", "numeric", "text"
}

// listColumnsWithTypes returns the ordered user columns for `table`
// plus each column's format_type — skipping system columns and
// STORED generated columns.
func listColumnsWithTypes(ctx context.Context, conn *pgx.Conn, table string) ([]columnInfo, error) {
	rows, err := conn.Query(ctx, `
		SELECT a.attname,
		       format_type(a.atttypid, NULL) AS type
		  FROM pg_attribute a
		  JOIN pg_class c ON c.oid = a.attrelid
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = 'public'
		   AND c.relname = $1
		   AND a.attnum > 0
		   AND NOT a.attisdropped
		   AND a.attgenerated = ''
		 ORDER BY a.attnum
	`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []columnInfo
	for rows.Next() {
		var c columnInfo
		if err := rows.Scan(&c.name, &c.pgType); err != nil {
			return nil, err
		}
		cols = append(cols, c)
	}
	return cols, rows.Err()
}

// castToTextTypes is the set of Postgres types we cast to text in the
// SELECT so pgx doesn't parse them into Go values that sqlLiteral
// mishandles. JSONB is the leading motivator (scalar values fell
// through to `sqlLiteral(string)` with no `::jsonb` cast); numeric,
// interval, inet, etc. return pgtype wrappers that sqlLiteral's
// default fallback would `%v`-print into a Go-struct dump.
//
// Compared by the base type name in `format_type` (which strips
// `pg_catalog.` and normalises to canonical form).
var castToTextTypes = map[string]bool{
	"jsonb":                       true,
	"json":                        true,
	"numeric":                     true,
	"interval":                    true,
	"inet":                        true,
	"cidr":                        true,
	"macaddr":                     true,
	"macaddr8":                    true,
	"tsvector":                    true,
	"tsquery":                     true,
	"box":                         true,
	"circle":                      true,
	"line":                        true,
	"lseg":                        true,
	"path":                        true,
	"point":                       true,
	"polygon":                     true,
	"date":                        true, // pgtype.Date wrapper; text form is portable
	"time without time zone":      true,
	"time with time zone":         true,
	"timestamp without time zone": true, // timestamptz stays native (time.Time)
	"money":                       true,
}

// selectExpr for one column — casts troublesome types to text so
// pgx returns them as strings sqlLiteral can safely wrap. numeric()
// with a scale gets stripped down to "numeric" via a canonical form
// (format_type returns "numeric(10,2)" — we key on the type name
// prefix).
func selectExprFor(c columnInfo) string {
	if needsTextCast(c.pgType) {
		return quoteIdent(c.name) + "::text AS " + quoteIdent(c.name)
	}
	// Postgres array types show as "typname[]" — cast the whole thing
	// to text so we don't hit pgtype array decoding, which returns
	// []interface{} that sqlLiteral wouldn't format correctly.
	if strings.HasSuffix(c.pgType, "[]") {
		return quoteIdent(c.name) + "::text AS " + quoteIdent(c.name)
	}
	return quoteIdent(c.name)
}

func needsTextCast(pgType string) bool {
	if castToTextTypes[pgType] {
		return true
	}
	// numeric(P,S) / numeric(P) etc.
	if strings.HasPrefix(pgType, "numeric(") {
		return true
	}
	// timestamp/time with precision variants.
	if strings.HasPrefix(pgType, "timestamp(") || strings.HasPrefix(pgType, "time(") {
		// keep timestamptz native — the "with time zone" text form is
		// standard timestamptz. Everything else casts to text.
		return !strings.Contains(pgType, "with time zone")
	}
	return false
}

// wrapValue converts a raw pgx value into the form sqlLiteral will
// render correctly given the column's declared type. For jsonb/json
// the value came back as a text-cast string — wrap as jsonbValue so
// the `::jsonb` cast is emitted. For numeric/interval/inet/etc. we
// emit `'<value>'::<type>` so Postgres re-parses.
func wrapValue(c columnInfo, v interface{}) interface{} {
	if v == nil {
		return nil
	}
	baseType := c.pgType
	if i := strings.IndexByte(baseType, '('); i >= 0 {
		baseType = baseType[:i]
	}
	baseType = strings.TrimSuffix(baseType, "[]")
	switch baseType {
	case "jsonb", "json":
		if s, ok := v.(string); ok {
			return jsonbLiteral(s)
		}
	case "numeric", "money", "interval", "inet", "cidr", "macaddr", "macaddr8",
		"tsvector", "tsquery", "box", "circle", "line", "lseg",
		"path", "point", "polygon", "date":
		if s, ok := v.(string); ok {
			return typedLiteral{value: s, pgType: baseType}
		}
	case "time without time zone", "time with time zone",
		"timestamp without time zone":
		if s, ok := v.(string); ok {
			return typedLiteral{value: s, pgType: baseType}
		}
	}
	// Also cover the []-array case — the text-cast produces a string
	// like `{1,2,3}` that Postgres accepts on insert if the target
	// column is the matching array type.
	if strings.HasSuffix(c.pgType, "[]") {
		if s, ok := v.(string); ok {
			return typedLiteral{value: s, pgType: c.pgType}
		}
	}
	return v
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
		   AND c.relkind = 'r'
		   AND c.relname NOT LIKE 'pg\_%'
		   AND c.relpersistence = 'p'
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

// emitRowData enumerates public tables, orders by FK topology, and
// writes multi-row INSERTs for each. Returns the count of tables
// visited. Per-table row counts land in `counts`.
func emitRowData(ctx context.Context, conn *pgx.Conn, writer *sqlFileWriter, tableOnly string, counts map[string]int64) (int, error) {
	tables, err := listPublicTables(ctx, conn)
	if err != nil {
		return 0, fmt.Errorf("list tables: %w", err)
	}
	edges, err := listPublicFKEdges(ctx, conn)
	if err != nil {
		return 0, fmt.Errorf("list FK edges: %w", err)
	}
	ordered, err := sortTablesByFK(tables, edges)
	if err != nil {
		return 0, err
	}

	visited := 0
	for _, t := range ordered {
		if tableOnly != "" && t.name != tableOnly {
			continue
		}
		cols, err := listColumnsWithTypes(ctx, conn, t.name)
		if err != nil {
			return visited, fmt.Errorf("list columns for %s: %w", t.name, err)
		}
		if len(cols) == 0 {
			continue
		}
		n, err := emitOneTable(ctx, conn, writer, t.name, cols)
		if err != nil {
			return visited, fmt.Errorf("emit rows for %s: %w", t.name, err)
		}
		counts[t.name] = n
		visited++
	}
	return visited, nil
}

// emitOneTable reads every row from `table` and writes batched
// INSERTs via `writer`. Returns the row count.
func emitOneTable(ctx context.Context, conn *pgx.Conn, writer *sqlFileWriter, table string, cols []columnInfo) (int64, error) {
	selectParts := make([]string, len(cols))
	colNames := make([]string, len(cols))
	for i, c := range cols {
		selectParts[i] = selectExprFor(c)
		colNames[i] = c.name
	}
	q := fmt.Sprintf("SELECT %s FROM %s", strings.Join(selectParts, ", "), quoteIdent(table))
	rows, err := conn.Query(ctx, q)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	if err := writer.WriteStatement(fmt.Sprintf("-- Table: %s\n", table)); err != nil {
		return 0, err
	}

	var batch [][]interface{}
	var total int64
	flushBatch := func() error {
		if len(batch) == 0 {
			return nil
		}
		var buf strings.Builder
		emitInsertBatch(&buf, table, colNames, batch)
		if err := writer.WriteStatement(buf.String()); err != nil {
			return err
		}
		batch = batch[:0]
		return nil
	}
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return total, err
		}
		row := make([]interface{}, len(cols))
		for i, v := range vals {
			row[i] = wrapValue(cols[i], coerceValue(v))
		}
		batch = append(batch, row)
		if len(batch) >= dataBatchSize {
			if err := flushBatch(); err != nil {
				return total, err
			}
		}
		total++
	}
	if err := rows.Err(); err != nil {
		return total, err
	}
	if err := flushBatch(); err != nil {
		return total, err
	}
	if total == 0 {
		if err := writer.WriteStatement("-- (no rows)\n\n"); err != nil {
			return total, err
		}
	}
	return total, nil
}

// coerceValue adapts non-string pgx values that WON'T come through the
// text cast (UUIDs, decoded JSONB maps if any escape the cast). Most
// of the "silent-corruption" cases are now handled by needsTextCast /
// selectExprFor at query time.
func coerceValue(v interface{}) interface{} {
	switch t := v.(type) {
	case nil:
		return nil
	case [16]byte:
		return fmt.Sprintf("%x-%x-%x-%x-%x", t[0:4], t[4:6], t[6:8], t[8:10], t[10:16])
	case map[string]interface{}:
		b, err := json.Marshal(t)
		if err != nil {
			return jsonbLiteral("{}")
		}
		return jsonbLiteral(string(b))
	case []interface{}:
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
//
// Does NOT wrap in BEGIN/COMMIT — the tenant-migrations endpoint runs
// each migration file in its own transaction and rejects explicit
// transaction-control statements (#275 review ship-blocker #1).
func emitAuthData(ctx context.Context, conn *pgx.Conn, writer *sqlFileWriter) (int64, int64, []string, error) {
	if err := writer.WriteStatement("-- Auth users (auth.users → users)\n"); err != nil {
		return 0, 0, nil, err
	}

	users, notes, err := readAuthUsers(ctx, conn)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("read auth.users: %w", err)
	}
	var userBatch [][]interface{}
	flushUserBatch := func() error {
		if len(userBatch) == 0 {
			return nil
		}
		var buf strings.Builder
		emitInsertBatch(&buf, "users", authUserColumns, userBatch)
		if err := writer.WriteStatement(buf.String()); err != nil {
			return err
		}
		userBatch = userBatch[:0]
		return nil
	}
	for _, u := range users {
		row, rowNotes := translateAuthUser(u)
		notes = append(notes, prefixNotes(u.ID, rowNotes)...)
		userBatch = append(userBatch, userRowAsValues(row))
		if len(userBatch) >= dataBatchSize {
			if err := flushUserBatch(); err != nil {
				return int64(len(users)), 0, notes, err
			}
		}
	}
	if err := flushUserBatch(); err != nil {
		return int64(len(users)), 0, notes, err
	}

	if err := writer.WriteStatement("-- Auth identities (auth.identities → user_identities)\n"); err != nil {
		return int64(len(users)), 0, notes, err
	}
	identities, idNotes, err := readAuthIdentities(ctx, conn)
	notes = append(notes, idNotes...)
	if err != nil {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42P01" {
			return int64(len(users)), 0, notes, fmt.Errorf("read auth.identities: %w", err)
		}
		notes = append(notes, "auth.identities not found on source — OAuth link rows skipped")
		identities = nil
	}
	var identBatch [][]interface{}
	flushIdentBatch := func() error {
		if len(identBatch) == 0 {
			return nil
		}
		var buf strings.Builder
		emitInsertBatch(&buf, "user_identities", authIdentityColumns, identBatch)
		if err := writer.WriteStatement(buf.String()); err != nil {
			return err
		}
		identBatch = identBatch[:0]
		return nil
	}
	for _, i := range identities {
		row := translateAuthIdentity(i)
		identBatch = append(identBatch, identityRowAsValues(row))
		if len(identBatch) >= dataBatchSize {
			if err := flushIdentBatch(); err != nil {
				return int64(len(users)), int64(len(identities)), notes, err
			}
		}
	}
	if err := flushIdentBatch(); err != nil {
		return int64(len(users)), int64(len(identities)), notes, err
	}
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
// map. Rows with NULL provider_id are skipped with a note — a NULL
// there means we can't populate the `(provider, provider_user_id)`
// unique index target, and inserting `''` (as an earlier draft did)
// would violate the constraint the moment two such rows collide
// (#275 review H #8).
//
// Returns 42P01 (undefined_table) on projects that predate the
// identities table — caller treats that as "no rows".
func readAuthIdentities(ctx context.Context, conn *pgx.Conn) ([]supabaseIdentity, []string, error) {
	rows, err := conn.Query(ctx, `
		SELECT id::text, user_id::text, provider,
		       provider_id,
		       COALESCE(identity_data::text, '{}'),
		       last_sign_in_at, created_at, updated_at
		  FROM auth.identities
		 ORDER BY user_id, created_at
	`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var out []supabaseIdentity
	var notes []string
	skipped := 0
	for rows.Next() {
		var i supabaseIdentity
		var providerID *string
		if err := rows.Scan(
			&i.ID, &i.UserID, &i.Provider,
			&providerID, &i.IdentityData,
			&i.LastSignInAt, &i.CreatedAt, &i.UpdatedAt,
		); err != nil {
			return out, notes, err
		}
		if providerID == nil || *providerID == "" {
			skipped++
			continue
		}
		i.ProviderID = *providerID
		out = append(out, i)
	}
	if skipped > 0 {
		notes = append(notes, fmt.Sprintf("skipped %d auth.identities row(s) with NULL provider_id — would violate the (provider, provider_user_id) unique index", skipped))
	}
	return out, notes, rows.Err()
}
