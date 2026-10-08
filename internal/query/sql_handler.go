package query

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/eurobase/euroback/internal/sqllog"
)

// auditDDLFromSQL inspects the SQL the runner just executed for any DDL
// shapes and writes them to schema_changes. Closes #120: SQL that lands
// outside the typed handlers (console SQL editor, MCP runSQL, SDK
// runSQL) used to silently skip the migration-history audit. This pass
// catches the common shapes (CREATE/DROP TABLE, ADD/DROP/ALTER/RENAME
// COLUMN, RENAME TABLE, CREATE/DROP INDEX) without needing event-
// trigger superuser privileges that managed Postgres won't grant.
//
// Best-effort: detection misses (direct DB shell, weird syntax) and
// audit-write failures both stay silent in the user request path. The
// existing typed-handler logSchemaChange in ddl_handler.go covers the
// authoritative path for handler-driven DDL — this is the SQL-runner
// supplement.
func auditDDLFromSQL(r *http.Request, engine *QueryEngine, sql string) {
	projectID := ProjectIDFromContext(r.Context())
	if projectID == "" {
		// Without a project the schema_changes FK would reject the
		// insert. SDK paths set this via the apikey middleware; the
		// platform path via PlatformTenantContext. Both run before
		// these handlers — if we ever land here without it, log so
		// the gap is visible.
		slog.Debug("auditDDLFromSQL: no project_id in context, skipping")
		return
	}
	ops := DetectDDL(sql)
	for _, op := range ops {
		logSchemaChange(engine.pool, r, projectID, op.Action, op.TableName, op.ColumnName, op.Detail)
	}
}

// SQLRequest is the JSON body for POST /v1/db/sql.
type SQLRequest struct {
	SQL   string `json:"sql"`
	Limit int    `json:"limit,omitempty"`
	// ReadOnly opts the call into a read-only transaction even on the
	// platform endpoint. The MCP server sets this to true by default
	// (closes #165 / part of #164 mitigation) so a prompt-injected
	// query cannot mutate state. Writes raise SQLSTATE 25006 and roll
	// back. Honoured only when the handler-level forceReadOnly is
	// false; on the SDK path forceReadOnly already gates writes via
	// ValidateSelectOnly.
	ReadOnly bool `json:"read_only,omitempty"`
}

// SQLResponse is the JSON response for a successful SQL execution.
type SQLResponse struct {
	Columns         []string                 `json:"columns"`
	Rows            []map[string]interface{} `json:"rows"`
	RowCount        int                      `json:"row_count"`
	ExecutionTimeMs float64                  `json:"execution_time_ms"`
}

// HandleSQL returns an http.HandlerFunc for POST /v1/db/sql (SDK — read-only).
func HandleSQL(engine *QueryEngine) http.HandlerFunc {
	return handleSQLInternal(engine, true)
}

// HandlePlatformSQL returns an http.HandlerFunc for POST /platform/.../data/sql (console — full access).
func HandlePlatformSQL(engine *QueryEngine) http.HandlerFunc {
	return handleSQLInternal(engine, false)
}

// SQLTransactionRequest is the JSON body for the multi-statement endpoint.
// Each element of Statements is executed as its own statement inside a
// single transaction. Mirrors Neon's run_sql_transaction shape.
type SQLTransactionRequest struct {
	Statements []string `json:"statements"`
	Limit      int      `json:"limit,omitempty"`
	// ReadOnly wraps the whole transaction in SET TRANSACTION READ ONLY.
	// MCP-origin requests set this true to block prompt-injected writes
	// (closes part of #165).
	ReadOnly bool `json:"read_only,omitempty"`
}

// SQLTransactionResponse is the JSON response for HandlePlatformSQLTransaction.
type SQLTransactionResponse struct {
	Statements      []StatementResult `json:"statements"`
	StatementCount  int               `json:"statement_count"`
	ExecutionTimeMs float64           `json:"execution_time_ms"`
}

// HandlePlatformSQLTransaction returns an http.HandlerFunc for
// POST /platform/.../data/sql/transaction. The body is
// {"statements": ["...", "..."]} and each statement runs in its own
// pgx Exec/Query call, all inside one transaction. If any statement
// fails the whole transaction is rolled back.
func HandlePlatformSQLTransaction(engine *QueryEngine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		schema := SchemaFromContext(r.Context())
		if schema == "" {
			jsonError(w, "tenant context not available", http.StatusBadRequest)
			return
		}

		var req SQLTransactionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if len(req.Statements) == 0 {
			jsonError(w, "statements must be a non-empty array", http.StatusBadRequest)
			return
		}

		// Every statement goes into the SQL log (one row each, one INSERT per
		// request), including a request the checks below refuse.
		sl := sqllog.FromContext(r.Context())
		logEntry := sqllog.FromRequest(r, sqllog.SourceSQLTransaction)
		logEntry.ReadOnly = req.ReadOnly
		logProject := ProjectIDFromContext(r.Context())
		logAll := func(entryFor func(i int) sqllog.Entry) {
			items := make([]sqllog.Item, len(req.Statements))
			for i, stmt := range req.Statements {
				items[i] = sqllog.Item{Statement: stmt, Entry: entryFor(i)}
			}
			sl.RecordBatch(r.Context(), logProject, items)
		}
		with := func(outcome, detail string, dur, rows *int) sqllog.Entry {
			e := logEntry
			e.Outcome, e.Detail, e.DurationMs, e.RowCount = outcome, detail, dur, rows
			return e
		}
		refuseAt := func(at int, msg string) {
			logAll(func(i int) sqllog.Entry {
				if i == at {
					return with(sqllog.OutcomeRefused, msg, nil, nil)
				}
				return with(sqllog.OutcomeNotRun, fmt.Sprintf("statement %d was refused", at+1), nil, nil)
			})
			jsonError(w, fmt.Sprintf("statement %d: %s", at+1, msg), http.StatusBadRequest)
		}

		// Same rationale as the single-statement handler: run the
		// cross-schema validator on the platform path too, so a
		// migrator-owned qualified reference to public.* or another
		// tenant's schema can't slip through. Validate every statement
		// up-front — a rejection on statement 3 shouldn't leave
		// statements 1 and 2 committed.
		//
		// Uses the exemption-aware variant so RLS policy helpers
		// (public.is_service_role() etc.) and id-default helpers
		// (public.uuid_generate_v4()) stay usable — the console docs
		// and Table Editor actively generate those references.
		platformOpts := CrossSchemaOptions{AllowedPublicNames: PlatformSQLPublicAllowlist}
		for i, stmt := range req.Statements {
			if err := ValidateNoCrossSchemaRefsOpts(stmt, schema, platformOpts); err != nil {
				refuseAt(i, err.Error())
				return
			}
			// Same catalog guard as the single-statement path — a
			// multi-statement migration must not be a way around it.
			if err := ValidateNoCatalogRefs(stmt); err != nil {
				refuseAt(i, err.Error())
				return
			}
			// No role / session / privilege management on this path.
			if err := ValidateNoPrivilegeStatements(stmt); err != nil {
				refuseAt(i, err.Error())
				return
			}
		}

		start := time.Now()
		results, err := engine.ExecuteSQLTransaction(r.Context(), schema, req.Statements, req.Limit, req.ReadOnly)
		elapsed := time.Since(start)
		if err != nil {
			slog.Error("sql transaction failed", "error", err, "schema", schema, "completed", len(results), "total", len(req.Statements))
			ran := resultsByIndex(results)
			logAll(func(i int) sqllog.Entry { return with(txLogOutcome(i, ran, err)) })
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}

		ran := resultsByIndex(results)
		logAll(func(i int) sqllog.Entry { return with(txLogOutcome(i, ran, nil)) })

		// Closes #120 for the multi-statement path. Each statement
		// committed successfully (the whole tx did), so audit any DDL
		// shapes we can recognise.
		for _, stmt := range req.Statements {
			auditDDLFromSQL(r, engine, stmt)
		}

		jsonResponse(w, SQLTransactionResponse{
			Statements:      results,
			StatementCount:  len(results),
			ExecutionTimeMs: float64(elapsed.Microseconds()) / 1000.0,
		}, http.StatusOK)
	}
}

func handleSQLInternal(engine *QueryEngine, forceReadOnly bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		schema := SchemaFromContext(r.Context())
		if schema == "" {
			jsonError(w, "tenant context not available", http.StatusBadRequest)
			return
		}

		var req SQLRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}

		// Platform path (console, MCP, CLI): every statement goes into the
		// SQL log, including the ones the checks below refuse.
		logSQL := func(outcome, detail string, dur, rows *int) {}
		if !forceReadOnly {
			sl := sqllog.FromContext(r.Context())
			entry := sqllog.FromRequest(r, sqllog.SourceSQL)
			entry.ReadOnly = req.ReadOnly
			projectID := ProjectIDFromContext(r.Context())
			logSQL = func(outcome, detail string, dur, rows *int) {
				e := entry
				e.Outcome, e.Detail, e.DurationMs, e.RowCount = outcome, detail, dur, rows
				sl.Record(r.Context(), projectID, req.SQL, e)
			}
		}
		refuse := func(msg string) {
			logSQL(sqllog.OutcomeRefused, msg, nil, nil)
			jsonError(w, msg, http.StatusBadRequest)
		}

		// Effective read-only: the SDK path (forceReadOnly=true) is
		// always read-only; the platform path (forceReadOnly=false)
		// honours the body's `read_only` field. Closes part of #165:
		// the MCP server sets read_only=true on its outbound calls so
		// a prompt-injected query cannot mutate state.
		readOnly := forceReadOnly || req.ReadOnly

		// Cross-schema validation runs on BOTH paths. Originally only
		// the SDK (forceReadOnly=true) branch ran this check; the
		// platform path skipped it, but the platform path runs as
		// eurobase_migrator (owner of every schema) with no RLS on
		// public.*, so a qualified reference like
		// `SELECT * FROM tenant_<other>.users` or
		// `SELECT * FROM public.subscriptions` from the console SQL
		// endpoint would succeed. Moving the check outside the branch
		// closes that: the more-privileged path should have at least
		// as many guards as the less-privileged one. Qualified refs to
		// the caller's own tenant schema are still allowed (that's the
		// whole point of the endpoint).
		//
		// SDK path uses SDKPublicAllowlist (id-default helpers only —
		// no RLS helpers, SDK doesn't author policies). Platform path
		// uses PlatformSQLPublicAllowlist (adds the RLS helpers).
		// Both allowlists deliberately exclude Eurobase-owned table
		// names so cross-tenant table reads stay blocked. See the
		// allowlist doc comments in cross_schema.go for the full
		// rationale.
		allowlist := PlatformSQLPublicAllowlist
		if forceReadOnly {
			allowlist = SDKPublicAllowlist
		}
		if err := ValidateNoCrossSchemaRefsOpts(req.SQL, schema, CrossSchemaOptions{
			AllowedPublicNames: allowlist,
		}); err != nil {
			refuse(err.Error())
			return
		}

		// Both paths: refuse bare or qualified references to the
		// PostgreSQL system catalog. The cross-schema check above is
		// dot-keyed and cannot see a bare `pg_stat_activity`, which
		// resolves via the always-implicit pg_catalog search path and
		// exposes every same-role session's in-flight SQL (all SDK
		// tenants share eurobase_gateway; the platform path inherits
		// it via developer → migrator → gateway). Cross-tenant
		// disclosure — see ValidateNoCatalogRefs.
		if err := ValidateNoCatalogRefs(req.SQL); err != nil {
			refuse(err.Error())
			return
		}

		// Both paths: no role / session / privilege management.
		if err := ValidateNoPrivilegeStatements(req.SQL); err != nil {
			refuse(err.Error())
			return
		}

		if forceReadOnly {
			// SDK: validate SELECT-only. Platform path allows writes
			// against the caller's own tenant schema.
			if err := ValidateSelectOnly(req.SQL); err != nil {
				refuse(err.Error())
				return
			}
			// SDK: block reads of sensitive system-table columns
			// (e.g. users.password_hash). The typed REST paths are
			// guarded in the query engine; this raw-SQL sibling shares
			// the same public-key / end-user-JWT threat surface and
			// must get the same guard. Input scan here (pre-exec);
			// output scan below covers SELECT *. Service key is exempt
			// inside the guard.
			if err := guardSDKSQLInput(r.Context(), req.SQL); err != nil {
				refuse(err.Error())
				return
			}
			// Table-level guard: refuse any reference to a system table.
			// Closes the row-typed / JSON-wrap bypass (to_jsonb(u),
			// row_to_json(u), bare `SELECT u`, composite casts) that the
			// column-name scans can't see. SDK path only; service exempt.
			if err := guardSDKSQLTables(r.Context(), req.SQL); err != nil {
				refuse(err.Error())
				return
			}
		}

		// Guard: pgx Tx.Exec uses the extended query protocol, which runs
		// only the first statement in a multi-statement string and silently
		// drops the rest. Reject such input here so callers cannot mistake
		// a partial run for success.
		if HasMultipleStatements(req.SQL) {
			refuse("input contains multiple SQL statements; this endpoint accepts a single statement. " +
				"Use POST /platform/projects/{id}/data/sql/transaction with a JSON body " +
				`{"statements": ["...", "..."]}` + " to run a multi-statement migration in one transaction.")
			return
		}

		// Cap limit.
		maxRows := req.Limit
		if maxRows <= 0 || maxRows > 1000 {
			maxRows = 1000
		}

		start := time.Now()
		// forceReadOnly is the SDK-path discriminator, not the
		// effective readOnly (which is also true when platform-path
		// MCP opts into read-only). Narrowing search_path on the
		// platform path would break migrator queries that reference
		// RLS helpers via `public.*`.
		columns, rows, err := engine.ExecuteSQLWithOpts(r.Context(), schema, req.SQL, maxRows, ExecOptions{
			ReadOnly: readOnly,
			SDKPath:  forceReadOnly,
		})
		elapsed := time.Since(start)

		if err != nil {
			slog.Error("sql execution failed", "error", err, "schema", schema)
			logSQL(sqllog.OutcomeError, err.Error(), sqllog.Ms(elapsed), nil)
			// The project's per-project login couldn't be opened (not
			// configured, or a Team project's dedicated DB not serving):
			// 503, never a fall-through to a shared login.
			if errors.Is(err, ErrTenantLoginUnavailable) {
				jsonError(w, "the project's database is not available right now", http.StatusServiceUnavailable)
				return
			}
			// Closes #52. SDK callers (readOnly path) get a sanitised
			// message — the raw pgx Error() output leaks Detail/Hint/
			// position which can echo offending values, internal paths,
			// or schema layout. Platform admins running ad-hoc SQL
			// through the console keep the full message because they
			// already see the schema.
			if readOnly {
				jsonError(w, sanitizeSDKSQLError(err), http.StatusBadRequest)
			} else {
				jsonError(w, err.Error(), http.StatusBadRequest)
			}
			return
		}

		// SDK output backstop: a `SELECT *` returns a denied column
		// under its real name without naming it in the SQL, so the
		// input scan above can't see it. Reject on the returned column
		// set. Service key is exempt inside the guard.
		if forceReadOnly {
			if err := guardSDKSQLOutput(r.Context(), columns); err != nil {
				jsonError(w, err.Error(), http.StatusBadRequest)
				return
			}
		}

		if rows == nil {
			rows = make([]map[string]interface{}, 0)
		}

		// Closes #120 for the single-statement path. SDK (read-only)
		// can't issue DDL via this endpoint — ValidateSelectOnly above
		// rejects it — so we only need to audit when the caller had
		// write access.
		if !readOnly {
			auditDDLFromSQL(r, engine, req.SQL)
		}

		resp := SQLResponse{
			Columns:         columns,
			Rows:            rows,
			RowCount:        len(rows),
			ExecutionTimeMs: float64(elapsed.Microseconds()) / 1000.0,
		}

		logSQL(sqllog.OutcomeOK, "", sqllog.Ms(elapsed), sqllog.Int(len(rows)))
		slog.Debug("sql query complete", "schema", schema, "rows", len(rows), "ms", resp.ExecutionTimeMs)
		jsonResponse(w, resp, http.StatusOK)
	}
}

// txLogOutcome is the SQL log outcome of input statement i of a
// transaction, given the statements that ran and the error (nil = the
// transaction committed). On error the whole transaction rolled back: a
// StatementError names the failing input position; any other error (setup,
// commit) applies to every statement.
func txLogOutcome(i int, ran ranStatements, err error) (outcome, detail string, dur, rows *int) {
	if err == nil {
		return sqllog.OutcomeOK, "", ran.duration(i), ran.rows(i)
	}
	var stmtErr *StatementError
	if !errors.As(err, &stmtErr) {
		return sqllog.OutcomeError, "transaction not committed: " + err.Error(), ran.duration(i), nil
	}
	switch {
	case i == stmtErr.Index:
		return sqllog.OutcomeError, err.Error(), nil, nil
	case i < stmtErr.Index:
		return sqllog.OutcomeError, fmt.Sprintf("rolled back: statement %d failed", stmtErr.Index+1), ran.duration(i), nil
	default:
		return sqllog.OutcomeNotRun, fmt.Sprintf("statement %d failed", stmtErr.Index+1), nil, nil
	}
}

// ranStatements maps ExecuteSQLTransaction results by input position
// (blank input elements have none).
type ranStatements map[int]StatementResult

func resultsByIndex(results []StatementResult) ranStatements {
	m := make(ranStatements, len(results))
	for _, r := range results {
		m[r.Index] = r
	}
	return m
}

func (m ranStatements) duration(i int) *int {
	r, ok := m[i]
	if !ok {
		return nil
	}
	return sqllog.Ms(time.Duration(r.ExecutionTimeMs * float64(time.Millisecond)))
}

func (m ranStatements) rows(i int) *int {
	r, ok := m[i]
	if !ok {
		return nil
	}
	return sqllog.Int(int(r.RowCount))
}
