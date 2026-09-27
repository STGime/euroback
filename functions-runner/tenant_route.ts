// Where a project's tenant data lives (#676).
//
// A Team / Legal-Team project keeps its tenant schema on a dedicated
// Postgres instance; everything else on the shared cluster. The runner
// asks public.runner_get_tenant_db(project) (migration 000131: host / port
// / database of the project's live project_databases row, no credentials)
// and connects there as <schema>_func. It never falls back to the shared
// cluster for a Team project: that has no schema for it, or a stale copy
// after a Pro→Team upgrade.

export interface DedicatedTarget {
  /** project_databases.id — part of the password derivation. */
  id: string;
  host: string;
  port: number;
  database: string;
}

export type TenantRoute =
  | { kind: "shared" }
  | { kind: "dedicated"; target: DedicatedTarget }
  | { kind: "not_ready"; state: string };

export interface TenantDbRow {
  id?: string | null;
  host: string | null;
  port: number | null;
  database_name: string | null;
  state: string;
}

/** Maps runner_get_tenant_db's result to a route (no row → shared cluster). */
export function routeFromRows(rows: TenantDbRow[] | undefined | null): TenantRoute {
  const row = rows?.[0];
  if (!row) return { kind: "shared" };
  if (row.state !== "active" || !row.id || !row.host || !row.port || !row.database_name) {
    return { kind: "not_ready", state: row.state };
  }
  return {
    kind: "dedicated",
    target: { id: row.id, host: row.host, port: Number(row.port), database: row.database_name },
  };
}

/**
 * Connection URL for <role> on a dedicated instance. TLS required
 * (Scaleway managed Postgres; same as the gateway's sslmode=require).
 */
export function dedicatedDbUrl(t: DedicatedTarget, role: string, password: string): string {
  const u = new URL("postgres://placeholder");
  u.hostname = t.host;
  u.port = String(t.port);
  u.pathname = "/" + encodeURIComponent(t.database);
  u.username = encodeURIComponent(role);
  u.password = encodeURIComponent(password);
  u.searchParams.set("sslmode", "require");
  return u.toString();
}

/** Stable cache key for a target (a project moved to another instance gets a new client). */
export function targetKey(t: DedicatedTarget): string {
  return `${t.id}@${t.host}:${t.port}/${t.database}`;
}

/**
 * Password derivation subject on a dedicated instance: per instance, so a
 * leaked Team password opens nothing else. Must match Go
 * tenantlogin.DedicatedSubject.
 */
export function dedicatedSubject(t: DedicatedTarget, schema: string): string {
  return `ded:${t.id}:${schema}`;
}

/** Thrown for a Team project whose dedicated database isn't active yet. */
export class TenantDbNotReady extends Error {
  constructor(state: string) {
    super(`the project's database is not ready yet (${state}); retry shortly`);
    this.name = "TenantDbNotReady";
  }
}

/** Short-lived per-project cache of routes, so each invocation doesn't re-query. */
export class RouteCache {
  private entries = new Map<string, { route: TenantRoute; at: number }>();
  // Short: after a restore cutover or a move, a cached route still points
  // at the old instance until it expires (in-flight moves themselves are
  // reported as not ready by runner_get_tenant_db).
  constructor(private readonly ttlMs = 5_000, private readonly now: () => number = Date.now) {}

  async get(projectId: string, lookup: (projectId: string) => Promise<TenantDbRow[]>): Promise<TenantRoute> {
    const hit = this.entries.get(projectId);
    if (hit && this.now() - hit.at < this.ttlMs) return hit.route;
    const route = routeFromRows(await lookup(projectId));
    // A not-ready route isn't cached: provisioning may finish any moment.
    if (route.kind !== "not_ready") this.entries.set(projectId, { route, at: this.now() });
    if (this.entries.size > 10_000) this.entries.clear();
    return route;
  }
}
