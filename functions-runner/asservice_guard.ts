// Pure gate for ctx.db.asService() invocations. Returns an error
// message (string) if the call must be refused, or null if it may
// proceed. Extracted from server.ts's runDBSql so the enforcement
// contract can be unit-tested without a Postgres dependency.
//
// This is the authoritative check — the worker's ctx.db.asService()
// only exposes the elevated handle when allow_service_role is true on
// the function, but a stale worker code cache or a hand-crafted RPC
// message from user JS still lands here and must fail closed.

import { findPlatformSystemTableRef } from "./sql_scan.ts";

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
  // Platform-table fence. asService() flips app.end_user_role to
  // 'service', which unlocks any RLS policy branch keyed on
  // is_service_role() — including the policies on the per-tenant
  // platform system tables (users, refresh_tokens, email_tokens,
  // vault_secrets, storage_objects). <schema>_func has DML on those
  // tables via migration 000047, so without this fence a JWT-verified
  // asService() invocation could read all password_hashes, forge
  // refresh_tokens, or overwrite user passwords for any end-user in
  // the tenant. The primitive's intended use — approving rows in a
  // tenant-owned table — never needs to touch these.
  const ref = findPlatformSystemTableRef(query);
  if (ref !== null) {
    return `ctx.db.asService() cannot reference the platform system table "${ref}" — use the platform APIs (e.g. auth flows) for that surface`;
  }
  return null;
}
