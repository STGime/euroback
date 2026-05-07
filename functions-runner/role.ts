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
// the runner validates again so a forged X-Schema-Name header (until
// HMAC verification ships in PR 3c) cannot smuggle quote characters or
// SQL fragments.
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
