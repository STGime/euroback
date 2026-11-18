package cli

// `eurobase import supabase data` — read auth users and row data from
// a Supabase project and write migration files the developer applies to
// their Eurobase project with `eurobase migrations up`.
//
// The files hold, in this order: the users (`auth.users` → `users`,
// `auth.identities` → `user_identities`), then each public table's rows
// as multi-row INSERTs in foreign-key order, then the sequence
// positions. Users come first because app tables reference them.
//
// No BEGIN/COMMIT in a file: the migrations endpoint runs each file in
// its own transaction (with the service identity, which the platform's
// `users` tables require) and refuses transaction control in the body.
// Output is split at 450 KB per file because the endpoint caps a
// migration at 512 KB (`maxMigrationSQLBytes`).
//
// Everything is read in one REPEATABLE READ snapshot, so rows that
// reference each other match even while the source is in use.

import (
	"context"
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
		Short: "Write Supabase users and table rows as Eurobase migration files",
		Long: `Read auth users and row data from a Supabase project and write
migration files to apply to your Eurobase project. Run it after
` + "`import supabase schema`" + ` has been applied.

Env vars:
  SUPABASE_DB_URL   postgres:// URL of the Supabase project (required)

What the files contain, in order:
  1. Users: auth.users → ` + "`users`" + `, OAuth links (auth.identities) →
     ` + "`user_identities`" + `. Ids and bcrypt password hashes are kept, so your
     rows still point at the right user and passwords keep working.
  2. Rows of every table in the public schema, parents before children.
     Triggers on a table are switched off while its rows are inserted.
  3. Sequence positions, so new rows get ids after the imported ones.

Everything is read in one snapshot. The output is split into files of at
most 450 KB (a migration may be 512 KB); ` + "`eurobase migrations up`" + ` applies
them in order.

Not copied:
  - Sessions and refresh tokens: users sign in again after the cutover.
  - Users deleted on Supabase (soft delete) and SSO users.
  - MFA factors.

The command prints what it changed or left out (anonymous users,
temporary bans, passwords that can't be carried over). Read that list.

Files land under --migrations-dir (default ./migrations), named
<epoch>_<name>_part_NNN.sql. Apply them to an empty project: a second
run writes new files and would insert the same rows again.

Flags:
  --skip-auth   only table rows, no users (rows that reference a user
                fail to apply unless the users are already there)
  --skip-rows   only users
  --table       only this table's rows (implies no users)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
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

			// One snapshot for users and rows, and nothing is written to
			// the source.
			tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
			if err != nil {
				return fmt.Errorf("start read-only snapshot: %w", err)
			}
			defer tx.Rollback(ctx) //nolint:errcheck

			if err := os.MkdirAll(migrationsDir, 0o755); err != nil {
				return fmt.Errorf("create %s: %w", migrationsDir, err)
			}

			now := time.Now()
			writer := newSQLFileWriter(migrationsDir, migName, now, maxPartBytes)
			if tableOnly != "" {
				skipAuth = true
			}
			var notes []string

			// Users first: app tables reference them.
			if !skipAuth {
				nUsers, nIdent, authNotes, err := emitAuthData(ctx, tx, writer, now)
				if err != nil {
					return fmt.Errorf("users: %w", err)
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "Users: %d, OAuth links: %d\n", nUsers, nIdent)
				notes = append(notes, authNotes...)
			}

			counts := map[string]int64{}
			if !skipRows {
				n, rowNotes, err := emitRowData(ctx, tx, writer, tableOnly, counts)
				if err != nil {
					return fmt.Errorf("row data: %w", err)
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "Row data: %d table(s), %d row(s)\n", n, sumCounts(counts))
				notes = append(notes, rowNotes...)
				if skipAuth && tableOnly == "" {
					notes = append(notes, "--skip-auth: rows that reference a user fail to apply unless the users are already in the project")
				}
			}
			if len(notes) > 0 {
				fmt.Fprintln(cmd.ErrOrStderr(), "\n── Read before applying: ──")
				for _, note := range notes {
					fmt.Fprintf(cmd.ErrOrStderr(), "  - %s\n", note)
				}
				fmt.Fprintln(cmd.ErrOrStderr())
			}

			paths, err := writer.Close()
			if err != nil {
				return fmt.Errorf("finalise output: %w", err)
			}
			if len(paths) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "Nothing to write — the source project has no data (or the --skip flags left nothing).")
				return nil
			}
			for _, p := range paths {
				fmt.Fprintf(cmd.OutOrStdout(), "Migration written to %s\n", p)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Next: `eurobase migrations up` to apply (to a project that has the schema and no data yet).")
			return nil
		},
	}
	cmd.Flags().StringVar(&migrationsDir, "migrations-dir", "./migrations", "Directory to write the migration files")
	cmd.Flags().StringVar(&migName, "name", "data_from_supabase", "Migration file basename (before _part_NNN.sql)")
	cmd.Flags().BoolVar(&skipAuth, "skip-auth", false, "Only table rows, no users")
	cmd.Flags().BoolVar(&skipRows, "skip-rows", false, "Only users, no table rows")
	cmd.Flags().StringVar(&tableOnly, "table", "", "Only this public-schema table's rows")
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
		"-- Written by `eurobase import supabase data`: users and rows from Supabase.\n"+
			"-- Part %d, generated %s UTC.\n"+
			"-- Apply with: eurobase migrations up (to a project that has the schema and no data yet)\n"+
			"--\n"+
			"-- Sessions and refresh tokens are not copied: users sign in again.\n\n",
		w.partIdx+1, w.baseTime.UTC().Format(time.RFC3339),
	)
}

// ── reading the source ──────────────────────────────────────────────

// sourceQuerier is what the readers need; a pgx.Tx (the command reads
// everything in one snapshot) and a *pgx.Conn both satisfy it.
type sourceQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// columnInfo is one column to copy.
type columnInfo struct {
	name string
	// identityAlways: GENERATED ALWAYS AS IDENTITY. An INSERT that names
	// the column needs OVERRIDING SYSTEM VALUE.
	identityAlways bool
}

// listColumns returns the columns of a public table to copy, in order:
// no system, dropped or generated (STORED) columns.
func listColumns(ctx context.Context, q sourceQuerier, table string) ([]columnInfo, error) {
	rows, err := q.Query(ctx, `
		SELECT a.attname, a.attidentity = 'a'
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
		if err := rows.Scan(&c.name, &c.identityAlways); err != nil {
			return nil, err
		}
		cols = append(cols, c)
	}
	return cols, rows.Err()
}

// listPublicTables returns every ordinary table in the public schema,
// ordered by name (the FK sort happens later).
func listPublicTables(ctx context.Context, q sourceQuerier) ([]tableRef, error) {
	rows, err := q.Query(ctx, `
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
func listPublicFKEdges(ctx context.Context, q sourceQuerier) ([]fkEdge, error) {
	rows, err := q.Query(ctx, `
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

// tableTriggers describes a table's own (non-internal) triggers.
type tableTriggers struct {
	enabled  int      // triggers that would fire on INSERT of imported rows
	disabled []string // switched off on the source; must stay off
}

// listUserTriggers returns the user triggers per public table.
func listUserTriggers(ctx context.Context, q sourceQuerier) (map[string]*tableTriggers, error) {
	rows, err := q.Query(ctx, `
		SELECT c.relname, t.tgname, t.tgenabled = 'D'
		  FROM pg_trigger t
		  JOIN pg_class c ON c.oid = t.tgrelid
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = 'public' AND NOT t.tgisinternal
		 ORDER BY c.relname, t.tgname
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*tableTriggers{}
	for rows.Next() {
		var table, name string
		var off bool
		if err := rows.Scan(&table, &name, &off); err != nil {
			return nil, err
		}
		tt := out[table]
		if tt == nil {
			tt = &tableTriggers{}
			out[table] = tt
		}
		if off {
			tt.disabled = append(tt.disabled, name)
		} else {
			tt.enabled++
		}
	}
	return out, rows.Err()
}

// sequenceState is one public sequence and where it stands.
type sequenceState struct {
	name      string
	ownerTab  string // table that owns it (serial / identity column); "" for a standalone sequence
	lastValue int64
	isCalled  bool
}

// listSequences reads every public sequence's position. Imported rows
// carry their ids, so without this the first INSERT after the import
// would be handed an id that already exists.
func listSequences(ctx context.Context, q sourceQuerier) ([]sequenceState, error) {
	rows, err := q.Query(ctx, `
		SELECT s.relname, COALESCE(t.relname, '')
		  FROM pg_class s
		  JOIN pg_namespace n ON n.oid = s.relnamespace
		  LEFT JOIN pg_depend d
		         ON d.classid = 'pg_class'::regclass AND d.objid = s.oid
		        AND d.refclassid = 'pg_class'::regclass AND d.deptype IN ('a', 'i')
		  LEFT JOIN pg_class t ON t.oid = d.refobjid
		 WHERE n.nspname = 'public' AND s.relkind = 'S'
		 ORDER BY s.relname
	`)
	if err != nil {
		return nil, err
	}
	var out []sequenceState
	for rows.Next() {
		var st sequenceState
		if err := rows.Scan(&st.name, &st.ownerTab); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, st)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if err := q.QueryRow(ctx, "SELECT last_value, is_called FROM public."+quoteIdent(out[i].name)).
			Scan(&out[i].lastValue, &out[i].isCalled); err != nil {
			return nil, fmt.Errorf("read sequence %s: %w", out[i].name, err)
		}
	}
	return out, nil
}

// setvalStatement moves a sequence to where it stood on the source.
func setvalStatement(st sequenceState) string {
	// The sequence name is a regclass literal: quoted as an identifier,
	// then as a string.
	return fmt.Sprintf("SELECT setval(%s, %d, %t);\n", sqlLiteral(quoteIdent(st.name)), st.lastValue, st.isCalled)
}

// ── row data ────────────────────────────────────────────────────────

// emitRowData enumerates public tables, orders them by foreign key, and
// writes multi-row INSERTs for each, followed by the sequence positions.
// Returns the number of tables written and notes for the developer.
// Per-table row counts land in `counts`.
func emitRowData(ctx context.Context, q sourceQuerier, writer *sqlFileWriter, tableOnly string, counts map[string]int64) (int, []string, error) {
	tables, err := listPublicTables(ctx, q)
	if err != nil {
		return 0, nil, fmt.Errorf("list tables: %w", err)
	}
	edges, err := listPublicFKEdges(ctx, q)
	if err != nil {
		return 0, nil, fmt.Errorf("list FK edges: %w", err)
	}
	ordered, err := sortTablesByFK(tables, edges)
	if err != nil {
		return 0, nil, err
	}
	triggers, err := listUserTriggers(ctx, q)
	if err != nil {
		return 0, nil, fmt.Errorf("list triggers: %w", err)
	}
	sequences, err := listSequences(ctx, q)
	if err != nil {
		return 0, nil, fmt.Errorf("list sequences: %w", err)
	}

	var notes []string
	var paused []string
	visited := 0
	found := tableOnly == ""
	for _, t := range ordered {
		if tableOnly != "" && t.name != tableOnly {
			continue
		}
		found = true
		if platformTableNames[t.name] {
			notes = append(notes, fmt.Sprintf("table %s skipped: every Eurobase project already has a table with that name — rename it in Supabase (and in your app), then import again", t.name))
			continue
		}
		cols, err := listColumns(ctx, q, t.name)
		if err != nil {
			return visited, notes, fmt.Errorf("list columns for %s: %w", t.name, err)
		}
		if len(cols) == 0 {
			continue
		}
		n, err := emitOneTable(ctx, q, writer, t.name, cols, triggers[t.name])
		if err != nil {
			return visited, notes, fmt.Errorf("emit rows for %s: %w", t.name, err)
		}
		if n > 0 && triggers[t.name] != nil && triggers[t.name].enabled > 0 {
			paused = append(paused, t.name)
		}
		for _, st := range sequences {
			if st.ownerTab == t.name {
				if err := writer.WriteStatement(setvalStatement(st)); err != nil {
					return visited, notes, err
				}
			}
		}
		if err := writer.WriteStatement("\n"); err != nil {
			return visited, notes, err
		}
		counts[t.name] = n
		visited++
	}
	if !found {
		return visited, notes, fmt.Errorf("no table %q in the public schema", tableOnly)
	}
	if tableOnly == "" {
		var standalone strings.Builder
		for _, st := range sequences {
			if st.ownerTab == "" {
				standalone.WriteString(setvalStatement(st))
			}
		}
		if standalone.Len() > 0 {
			if err := writer.WriteStatement("-- Sequences not owned by a column\n" + standalone.String() + "\n"); err != nil {
				return visited, notes, err
			}
		}
	}
	if len(paused) > 0 {
		notes = append(notes, fmt.Sprintf("triggers are switched off while rows are inserted into: %s — the rows arrive as they are on Supabase (a trigger would write its audit rows or timestamps a second time)", strings.Join(paused, ", ")))
	}
	return visited, notes, nil
}

// emitOneTable reads every row from `table` and writes batched INSERTs
// via `writer`. Returns the row count.
//
// Every column is read as text and written as an untyped literal, so
// the target's own input function parses it — the form pg_dump uses,
// and the only one that round-trips every type (NaN, infinity, JSON
// null, bit(n), composites, domains, extension types) without a list
// of special cases.
func emitOneTable(ctx context.Context, q sourceQuerier, writer *sqlFileWriter, table string, cols []columnInfo, trg *tableTriggers) (int64, error) {
	selectParts := make([]string, len(cols))
	colNames := make([]string, len(cols))
	overriding := false
	for i, c := range cols {
		selectParts[i] = quoteIdent(c.name) + "::text"
		colNames[i] = c.name
		overriding = overriding || c.identityAlways
	}
	rows, err := q.Query(ctx, fmt.Sprintf("SELECT %s FROM public.%s", strings.Join(selectParts, ", "), quoteIdent(table)))
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	if err := writer.WriteStatement(fmt.Sprintf("-- Table: %s\n", table)); err != nil {
		return 0, err
	}

	// User triggers stay off while a batch is inserted; triggers that
	// were off on the source are switched off again afterwards.
	var before, after string
	if trg != nil && trg.enabled > 0 {
		before = fmt.Sprintf("ALTER TABLE %s DISABLE TRIGGER USER;\n", quoteIdent(table))
		after = fmt.Sprintf("ALTER TABLE %s ENABLE TRIGGER USER;\n", quoteIdent(table))
		for _, name := range trg.disabled {
			after += fmt.Sprintf("ALTER TABLE %s DISABLE TRIGGER %s;\n", quoteIdent(table), quoteIdent(name))
		}
	}

	var batch [][]interface{}
	var batchBytes int
	var total int64
	flushBatch := func() error {
		if len(batch) == 0 {
			return nil
		}
		var buf strings.Builder
		buf.WriteString(before)
		emitInsert(&buf, table, colNames, batch, overriding)
		buf.WriteString(after)
		if err := writer.WriteStatement(buf.String()); err != nil {
			return err
		}
		batch = batch[:0]
		batchBytes = 0
		return nil
	}
	dest := make([]interface{}, len(cols))
	for rows.Next() {
		vals := make([]*string, len(cols))
		for i := range vals {
			dest[i] = &vals[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return total, err
		}
		row := make([]interface{}, len(cols))
		for i, v := range vals {
			if v != nil {
				row[i] = *v
			}
		}
		rowBytes := estimateRowBytes(row)
		// Flush BEFORE appending if this row would tip the batch over
		// either ceiling: the byte cap is what the endpoint enforces,
		// the row cap covers tables of tiny rows.
		if len(batch) > 0 && (len(batch) >= dataBatchSize || batchBytes+rowBytes > maxBatchBytes) {
			if err := flushBatch(); err != nil {
				return total, err
			}
		}
		batch = append(batch, row)
		batchBytes += rowBytes
		total++
	}
	if err := rows.Err(); err != nil {
		return total, err
	}
	if err := flushBatch(); err != nil {
		return total, err
	}
	if total == 0 {
		if err := writer.WriteStatement("-- (no rows)\n"); err != nil {
			return total, err
		}
	}
	return total, nil
}

// ── auth users ──────────────────────────────────────────────────────

// emitAuthData reads Supabase's auth.users + auth.identities, translates
// them, and writes the INSERTs. Returns user + identity counts and notes
// for the developer.
//
// No BEGIN/COMMIT: the migrations endpoint runs each file in its own
// transaction and refuses transaction control in the body.
func emitAuthData(ctx context.Context, q sourceQuerier, writer *sqlFileWriter, now time.Time) (int64, int64, []string, error) {
	users, err := readAuthUsers(ctx, q)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("read auth.users: %w", err)
	}
	identities, idNotes, err := readAuthIdentities(ctx, q)
	if err != nil {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42P01" {
			return 0, 0, nil, fmt.Errorf("read auth.identities: %w", err)
		}
		idNotes = append(idNotes, "auth.identities not found on source — OAuth links skipped")
		identities = nil
	}
	plan, err := planAuthImport(users, identities, now)
	if err != nil {
		return 0, 0, nil, err
	}
	notes := append(idNotes, plan.notes...)

	if err := writer.WriteStatement("-- Auth users (auth.users → users)\n"); err != nil {
		return 0, 0, notes, err
	}
	userRows := make([][]interface{}, len(plan.users))
	for i, u := range plan.users {
		userRows[i] = userRowAsValues(u)
	}
	if err := writeBatched(writer, "users", authUserColumns, userRows); err != nil {
		return 0, 0, notes, err
	}
	if err := writer.WriteStatement("\n-- OAuth links (auth.identities → user_identities)\n"); err != nil {
		return 0, 0, notes, err
	}
	identRows := make([][]interface{}, len(plan.identities))
	for i, id := range plan.identities {
		identRows[i] = identityRowAsValues(id)
	}
	if err := writeBatched(writer, "user_identities", authIdentityColumns, identRows); err != nil {
		return 0, 0, notes, err
	}
	if err := writer.WriteStatement("\n"); err != nil {
		return 0, 0, notes, err
	}
	return int64(len(plan.users)), int64(len(plan.identities)), notes, nil
}

// writeBatched writes rows as INSERT statements that each stay under the
// endpoint's size cap.
func writeBatched(writer *sqlFileWriter, table string, columns []string, rows [][]interface{}) error {
	var batch [][]interface{}
	var batchBytes int
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		var buf strings.Builder
		emitInsertBatch(&buf, table, columns, batch)
		batch, batchBytes = batch[:0], 0
		return writer.WriteStatement(buf.String())
	}
	for _, row := range rows {
		rowBytes := estimateRowBytes(row)
		if len(batch) > 0 && (len(batch) >= dataBatchSize || batchBytes+rowBytes > maxBatchBytes) {
			if err := flush(); err != nil {
				return err
			}
		}
		batch = append(batch, row)
		batchBytes += rowBytes
	}
	return flush()
}

// authColumnPresent reports which of the optional auth.users columns the
// source has (older Supabase projects lack some).
func authColumnPresent(ctx context.Context, q sourceQuerier, table string) (map[string]bool, error) {
	rows, err := q.Query(ctx, `
		SELECT a.attname
		  FROM pg_attribute a
		 WHERE a.attrelid = to_regclass($1) AND a.attnum > 0 AND NOT a.attisdropped`, "auth."+table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

// readAuthUsers reads every auth.users row with the columns we map.
// Which users are imported is decided in planAuthImport.
func readAuthUsers(ctx context.Context, q sourceQuerier) ([]supabaseUser, error) {
	have, err := authColumnPresent(ctx, q, "users")
	if err != nil {
		return nil, err
	}
	if len(have) == 0 {
		return nil, errors.New("auth.users not found — is SUPABASE_DB_URL a Supabase project? (use --skip-auth to copy only the tables)")
	}
	opt := func(col, fallback string) string {
		if have[col] {
			return col
		}
		return fallback
	}
	rows, err := q.Query(ctx, fmt.Sprintf(`
		SELECT id::text, email, encrypted_password,
		       email_confirmed_at, %s, %s,
		       last_sign_in_at, %s,
		       COALESCE(raw_user_meta_data::text, '{}'),
		       COALESCE(raw_app_meta_data::text, '{}'),
		       created_at, updated_at,
		       %s, COALESCE(%s, false), COALESCE(%s, false)
		  FROM auth.users
		 ORDER BY created_at, id`,
		opt("phone", "NULL::text"), opt("phone_confirmed_at", "NULL::timestamptz"),
		opt("banned_until", "NULL::timestamptz"),
		opt("deleted_at", "NULL::timestamptz"), opt("is_anonymous", "false"), opt("is_sso_user", "false")))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []supabaseUser
	for rows.Next() {
		var u supabaseUser
		var createdAt *time.Time
		if err := rows.Scan(
			&u.ID, &u.Email, &u.EncryptedPassword,
			&u.EmailConfirmedAt, &u.Phone, &u.PhoneConfirmedAt,
			&u.LastSignInAt, &u.BannedUntil,
			&u.RawUserMetaData, &u.RawAppMetaData,
			&createdAt, &u.UpdatedAt,
			&u.DeletedAt, &u.IsAnonymous, &u.IsSSOUser,
		); err != nil {
			return out, err
		}
		if createdAt != nil {
			u.CreatedAt = *createdAt
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// readAuthIdentities reads the auth.identities columns we map. Rows
// without a provider_id are skipped with a note: they can't fill the
// (provider, provider_user_id) key.
//
// Returns 42P01 (undefined_table) on projects that predate the
// identities table — the caller treats that as "no rows".
func readAuthIdentities(ctx context.Context, q sourceQuerier) ([]supabaseIdentity, []string, error) {
	rows, err := q.Query(ctx, `
		SELECT id::text, user_id::text, provider,
		       provider_id,
		       COALESCE(identity_data::text, '{}'),
		       last_sign_in_at, created_at, updated_at
		  FROM auth.identities
		 ORDER BY user_id, created_at, id
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
		var createdAt *time.Time
		if err := rows.Scan(
			&i.ID, &i.UserID, &i.Provider,
			&providerID, &i.IdentityData,
			&i.LastSignInAt, &createdAt, &i.UpdatedAt,
		); err != nil {
			return out, notes, err
		}
		if providerID == nil || *providerID == "" {
			skipped++
			continue
		}
		if createdAt != nil {
			i.CreatedAt = *createdAt
		}
		i.ProviderID = *providerID
		out = append(out, i)
	}
	if skipped > 0 {
		notes = append(notes, fmt.Sprintf("%d OAuth link(s) without a provider id skipped", skipped))
	}
	return out, notes, rows.Err()
}
