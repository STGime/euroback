// Per-tenant database connections for ctx.db.sql (stage B, phase 1a).
//
// When FUNC_PASSWORD_SECRET is set, customer SQL runs on a connection
// that *logs in as* the tenant's `<schema>_func` role (password derived
// the same way as internal/tenantlogin.FuncPassword), so the session
// identity itself is the tenant. Without the secret the runner keeps the
// legacy shared-connection role switch.
//
// Budget (shared cluster, max_connections=100): one connection per
// tenant, a global cap, idle close, LRU eviction.

const enc = new TextEncoder();

/** HMAC-SHA256(secret, "funcpw:" + schema), hex. Must match Go FuncPassword. */
export async function funcPassword(secret: string, schema: string): Promise<string> {
  const key = await crypto.subtle.importKey("raw", enc.encode(secret), { name: "HMAC", hash: "SHA-256" }, false, ["sign"]);
  const sig = new Uint8Array(await crypto.subtle.sign("HMAC", key, enc.encode("funcpw:" + schema)));
  return Array.from(sig, (b) => b.toString(16).padStart(2, "0")).join("");
}

/** Builds the tenant connection URL from the runner's own DB URL. */
export function tenantDbUrl(baseUrl: string, role: string, password: string): string {
  const u = new URL(baseUrl);
  u.username = encodeURIComponent(role);
  u.password = encodeURIComponent(password);
  return u.toString();
}

// deno-lint-ignore no-explicit-any
export type SqlClient = any;
export type ClientFactory = (url: string) => SqlClient;

interface Entry {
  client: SqlClient;
  lastUsed: number;
}

/** LRU cache of per-tenant clients with a global cap. */
export class TenantDBPool {
  private entries = new Map<string, Entry>();

  constructor(
    private readonly baseUrl: string,
    private readonly secret: string,
    private readonly factory: ClientFactory,
    private readonly cap = 20,
  ) {}

  get size(): number {
    return this.entries.size;
  }

  async get(schema: string, role: string): Promise<SqlClient> {
    const hit = this.entries.get(schema);
    if (hit) {
      hit.lastUsed = Date.now();
      return hit.client;
    }
    if (this.entries.size >= this.cap) {
      let oldest: string | null = null;
      let oldestAt = Infinity;
      for (const [k, v] of this.entries) {
        if (v.lastUsed < oldestAt) {
          oldest = k;
          oldestAt = v.lastUsed;
        }
      }
      if (oldest !== null) await this.evict(oldest);
    }
    const pw = await funcPassword(this.secret, schema);
    const client = this.factory(tenantDbUrl(this.baseUrl, role, pw));
    this.entries.set(schema, { client, lastUsed: Date.now() });
    return client;
  }

  async evict(schema: string): Promise<void> {
    const e = this.entries.get(schema);
    if (!e) return;
    this.entries.delete(schema);
    try {
      await e.client.end({ timeout: 5 });
    } catch {
      // closing a broken client is best-effort
    }
  }
}

/** Postgres auth-failure SQLSTATEs (password / role not yet loginable). */
export function isAuthError(err: unknown): boolean {
  // deno-lint-ignore no-explicit-any
  const code = (err as any)?.code;
  return code === "28P01" || code === "28000";
}
