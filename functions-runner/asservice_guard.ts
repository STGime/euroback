// Pure gate for ctx.db.asService() invocations. Returns an error
// message (string) if the call must be refused, or null if it may
// proceed. Extracted from server.ts's runDBSql so the enforcement
// contract can be unit-tested without a Postgres dependency.
//
// This is the authoritative check — the worker's ctx.db.asService()
// only exposes the elevated handle when allow_service_role is true on
// the function, but a stale worker code cache or a hand-crafted RPC
// message from user JS still lands here and must fail closed.

import { findPlatformSystemTableRef, scanIdentifiersAndDots, TokKind } from "./sql_scan.ts";

// ALLOWED_STATEMENT_KINDS is the set of first-keyword statement types
// asService accepts. Anything else — DO, CREATE, PREPARE, EXECUTE,
// DECLARE, COPY, SET, RESET, BEGIN, COMMIT, LISTEN, VACUUM, TABLE,
// VALUES, etc. — is refused so that the fence isn't bypassed via
// procedural bodies (`DO $$ ... $$` runs as the current role and can
// touch every table `<schema>_func` has grants on), prepared plans,
// or session-state manipulation. WITH is included because CTE-DML
// (`WITH x AS (...) INSERT ...`) is the canonical shape for
// approve-and-write flows.
const ALLOWED_STATEMENT_KINDS: ReadonlySet<string> = new Set([
  "select",
  "insert",
  "update",
  "delete",
  "with",
]);

// firstKeyword returns the lowercased first identifier in `sql`,
// skipping leading whitespace and comments. Reuses the same scanner
// as the platform-table fence so string / comment / dollar-quote
// skip semantics stay consistent.
function firstKeyword(sql: string): string {
  for (const tok of scanIdentifiersAndDots(sql)) {
    if (tok.kind === TokKind.Ident) return tok.value.toLowerCase();
  }
  return "";
}

// DOLLAR_BODY_RE matches PostgreSQL dollar-quoted string bodies —
// `$$ ... $$` and `$tag$ ... $tag$`. Deliberately does NOT match
// numbered parameter placeholders (`$1`, `$2`) because those require
// a digit after `$`, whereas a dollar-tag requires a letter or
// underscore.
//
// Why refuse them wholesale on asService: the platform-table scanner
// skips dollar-quoted regions by design (mirrors the SDK-path
// scanner), so a `DO $$ BEGIN UPDATE users ... END $$` block executes
// under `<schema>_func` — which has DML on the tenant's system tables
// — with the caller's `end_user_role='service'` GUC still set. The
// same reasoning applies to `CREATE FUNCTION ... AS $$ ... $$` and
// `PREPARE p AS $$ ... $$`. The DML-only allowlist above already
// blocks the outer keywords (DO / CREATE / PREPARE), but the body
// check is defence-in-depth: a future statement kind that legitimately
// belongs on the allowlist (say WITH someday supports dollar-bodies
// via an extension) cannot regress the fence.
const DOLLAR_BODY_RE = /\$[A-Za-z_][A-Za-z0-9_]*\$|\$\$/;

// U_ESCAPE_IDENT_RE matches PostgreSQL Unicode-escaped identifiers
// (`U&"\0075sers"` — which resolves to `"users"` at parse time). The
// lexical scanner never decodes these, so the platform-table fence
// wouldn't see `users` in that spelling. Also refuses the string-
// literal form (`U&'...'`) so intent alone can't route through it.
const U_ESCAPE_RE = /u&\s*["']/i;

// checkAsServiceQuery returns null when the query may proceed, or a
// human-readable error message the runner should throw back to the
// worker's RPC layer (which surfaces it as an Error inside user JS).
//
// mode: undefined = regular ctx.db.sql — never gated here (RLS applies
//   normally); "service" = ctx.db.asService().sql, which must be gated.
// allowServiceRole: the DB-side opt-in flag from edge_functions.
// query: the SQL string; used for the statement-kind + fence checks.
//
// Check order (matters for developer experience):
//   1. Opt-in — most actionable error message.
//   2. Statement kind — before scanning the body, refuse the shape
//      that could hide bypasses.
//   3. Dollar-quoted body — defence-in-depth if (2) is ever relaxed.
//   4. Unicode-escaped identifier — closes the scanner's non-decode.
//   5. Platform-table reference — the direct-name fence.

// checkAsServiceQuery returns null when the query may proceed, or a
// human-readable error message the runner should throw back to the
// worker's RPC layer (which surfaces it as an Error inside user JS).
//
// mode: undefined = regular ctx.db.sql — never gated here (RLS applies
//   normally); "service" = ctx.db.asService().sql, which must be gated.
// allowServiceRole: the DB-side opt-in flag from edge_functions.
// query: the SQL string; only used to check for platform-table refs.
export function checkAsServiceQuery(
  mode: "service" | undefined,
  allowServiceRole: boolean,
  query: string,
): string | null {
  if (mode !== "service") return null;
  if (!allowServiceRole) {
    return "ctx.db.asService() is not enabled for this function — set allow_service_role=true on the function to opt in";
  }
  const kind = firstKeyword(query);
  if (!ALLOWED_STATEMENT_KINDS.has(kind)) {
    return `ctx.db.asService() only accepts SELECT/INSERT/UPDATE/DELETE/WITH statements (got ${
      kind.toUpperCase() || "<empty>"
    })`;
  }
  if (DOLLAR_BODY_RE.test(query)) {
    return "ctx.db.asService() does not accept dollar-quoted bodies ($$ or $tag$) — pass values as $1, $2, ... parameters";
  }
  if (U_ESCAPE_RE.test(query)) {
    return "ctx.db.asService() does not accept Unicode-escaped literals or identifiers (U&\"...\" / U&'...')";
  }
  // Platform-table fence. asService() flips app.end_user_role to
  // 'service', which unlocks any RLS policy branch keyed on
  // is_service_role() — including the policies on the per-tenant
  // platform system tables (users, refresh_tokens, email_tokens,
  // vault_secrets, storage_objects). <schema>_func has DML on those
  // tables via migration 000047, so without this fence a JWT-verified
  // asService() invocation could read all password_hashes, forge
  // refresh_tokens, or overwrite user passwords for any end-user in
  // the tenant. The primitive's intended use — approving rows in a
  // tenant-owned table — never needs to touch these. This is the
  // direct-reference fence; the follow-up grant-level REVOKE on
  // <schema>_func is what makes the guarantee dynamic-SQL-proof
  // (tracked separately).
  const ref = findPlatformSystemTableRef(query);
  if (ref !== null) {
    return `ctx.db.asService() cannot reference the platform system table "${ref}" — use the platform APIs (e.g. auth flows) for that surface`;
  }
  return null;
}
