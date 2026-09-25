// Per-tenant database connections for ctx.db.sql (stage B).
//
// When FUNC_PASSWORD_SECRET is set, customer SQL runs on a connection
// that *logs in as* the tenant's `<schema>_func` role (password derived
// the same way as internal/tenantlogin.FuncPassword), so the session
// identity itself is the tenant. There is no shared-connection fallback
// (phase 1b): the runner's own login is a member of no tenant role.
//
// Budget (shared cluster, max_connections=100): one connection per
// tenant, a soft global cap, idle close, LRU eviction of *idle* entries
// only — a connection with a transaction in flight is never closed.

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
  client: Promise<SqlClient>;
  lastUsed: number;
  inUse: number;
}

export interface Lease {
  client: SqlClient;
  release: () => void;
  /** Drops this lease's client (e.g. after a failed login) if it is still the cached one. */
  invalidate: () => void;
}

export class TenantDBPool {
  private entries = new Map<string, Entry>();

  constructor(
    private readonly baseUrl: string,
    private readonly secret: string,
    private readonly factory: ClientFactory,
    private readonly cap = 20,
    private readonly now: () => number = Date.now,
  ) {}

  get size(): number {
    return this.entries.size;
  }

  /**
   * Leases the tenant's client. Concurrent first calls share one client;
   * the caller must release() when its transaction has finished.
   */
  async acquire(schema: string, role: string): Promise<Lease> {
    let entry = this.entries.get(schema);
    if (!entry) {
      this.evictIdleOverCap();
      const client = funcPassword(this.secret, schema).then((pw) => this.factory(tenantDbUrl(this.baseUrl, role, pw)));
      entry = { client, lastUsed: this.now(), inUse: 0 };
      this.entries.set(schema, entry);
    }
    entry.inUse++;
    entry.lastUsed = this.now();
    const e = entry;
    let released = false;
    try {
      const client = await e.client;
      return {
        client,
        release: () => {
          if (released) return;
          released = true;
          e.inUse--;
          e.lastUsed = this.now();
        },
        invalidate: () => {
          if (this.entries.get(schema) === e) this.drop(schema);
        },
      };
    } catch (err) {
      e.inUse--;
      if (this.entries.get(schema) === e) this.entries.delete(schema);
      throw err;
    }
  }

  /** Evicts least-recently-used *idle* entries while at/over the cap. */
  private evictIdleOverCap(): void {
    while (this.entries.size >= this.cap) {
      let oldest: string | null = null;
      let oldestAt = Infinity;
      for (const [k, v] of this.entries) {
        if (v.inUse === 0 && v.lastUsed < oldestAt) {
          oldest = k;
          oldestAt = v.lastUsed;
        }
      }
      if (oldest === null) return; // all busy: soft cap, idle_timeout reclaims later
      this.drop(oldest);
    }
  }

  /** Removes an entry and closes its client in the background once idle. */
  private drop(schema: string): void {
    const e = this.entries.get(schema);
    if (!e) return;
    this.entries.delete(schema);
    const close = () =>
      e.client.then((c) => c.end({ timeout: 5 })).catch(() => {
        // closing a broken client is best-effort
      });
    if (e.inUse === 0) {
      close();
    } else {
      // In-flight transactions finish; postgres.js idle_timeout then
      // closes the connection. Never kill a running transaction.
    }
  }
}

/** Postgres auth-failure SQLSTATEs (password / role not yet loginable). */
export function isAuthError(err: unknown): boolean {
  // deno-lint-ignore no-explicit-any
  const code = (err as any)?.code;
  return code === "28P01" || code === "28000";
}
