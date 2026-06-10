// Per-tenant role helpers for the function runner.
//
// Closes advisory GHSA-7428-mvpp-rhr7 (C3) layer 1.
//
// The runner is a long-running process shared across tenants. Each
// invocation must execute SQL under a role that has grants only on the
// caller's tenant schema, so a malicious function calling
// `ctx.db.sql("SELECT * FROM tenant_other.users")` fails with permission
// denied at the Postgres level (not just at search_path).
//
// These helpers are extracted from server.ts so they can be unit-tested
// without spinning up a Deno HTTP server or Postgres.

// validIdentRe matches Postgres identifiers safe to interpolate into
// SQL via double-quote wrapping. Letters, digits, underscores; leads
// with letter/underscore; max 63 bytes (Postgres's NAMEDATALEN-1
// default). Schemas the gateway forwards always conform to this shape;
// gateway→runner traffic is HMAC-signed and verified (see
// authenticateRequest in server.ts), and the runner validates the shape
// again as defence-in-depth so a forged X-Schema-Name header cannot
// smuggle quote characters or SQL fragments even if that signing is ever
// weakened by misconfiguration.
export const validIdentRe = /^[a-zA-Z_][a-zA-Z0-9_]{0,62}$/;

// tenantFuncRole returns the per-tenant Postgres role name. Created at
// tenant provisioning time by migration 000047. Naming convention:
// `<schema_name>_func`. NOLOGIN, INHERIT, granted USAGE+DML on its own
// schema only.
export function tenantFuncRole(schemaName: string): string {
  return schemaName + "_func";
}

// quoteIdent wraps a Postgres identifier in double quotes and escapes
// embedded double quotes, matching Postgres's identifier-quoting rules.
// Used to build `SET LOCAL ROLE "..."` and `SET LOCAL search_path TO "..."`
// statements. Always pair with validIdentRe.test() before use — quoteIdent
// alone does not protect against an attacker-controlled identifier with
// embedded quotes (it correctly escapes them, but only safe identifiers
// should reach this layer at all).
export function quoteIdent(s: string): string {
  return '"' + s.replaceAll('"', '""') + '"';
}

// rlsContextStatements returns the per-transaction set_config statements
// that mirror the gateway's RLS context (internal/query/engine.go,
// applyRLSContext) so auth_uid() / is_service_role() behave identically
// in edge functions and gateway REST (closes #188).
//
// With an end-user (gateway sets X-User-ID from verified claims, covered
// by the HMAC signature) the function acts as that user: policies see
// app.end_user_role='authenticated' and auth_uid() = the user. Without
// one, function code is developer-authored server-side code, so it gets
// the service branch — same as gateway secret-key traffic. The 'anon'
// branch is deliberately not mirrored: anonymous *end-user* access only
// makes sense for client-issued queries, not for tenant-deployed code.
//
// SECURITY: X-User-ID now drives the RLS *identity* (app.end_user_id +
// the authenticated role), not just ctx.user, so a forged value would
// impersonate that user at the row-security layer for every table. This
// is only safe because gateway→runner traffic is HMAC-signed (X-User-ID
// is in the canonical message) AND the runner runs in strict mode
// (FUNCTIONS_RUNNER_HMAC_REQUIRE_SIGNED=true in deploy/k8s/functions.yaml).
// Do not relax HMAC verification to soft mode without accounting for the
// fact that it now governs database row security, not only ctx.user.
export function rlsContextStatements(
  userId: string,
): Array<{ sql: string; params: string[] }> {
  if (userId) {
    return [
      {
        sql: "SELECT set_config('app.end_user_id', $1, true)",
        params: [userId],
      },
      {
        sql: "SELECT set_config('app.end_user_role', 'authenticated', true)",
        params: [],
      },
    ];
  }
  return [
    {
      sql: "SELECT set_config('app.end_user_role', 'service', true)",
      params: [],
    },
  ];
}
