import { assert, assertEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { funcPassword, isConnectionDrop, isLoginError, isPoolerBusyError, TenantDBPool, tenantDbUrl } from "./tenant_db.ts";

Deno.test("funcPassword matches the Go vector (internal/tenantlogin)", async () => {
  assertEquals(
    await funcPassword("0123456789abcdef0123456789abcdef", "tenant_abc"),
    "5f64bdf519eef7742b69146d0b0d9a1949f07a14b5292d5373ffce01ad74a95b",
  );
});

Deno.test("tenantDbUrl swaps credentials, keeps host/db/params", () => {
  const u = new URL(tenantDbUrl("postgres://runner:secret@db.internal:5432/eurobase?sslmode=require", "tenant_abc_func", "hexpw"));
  assertEquals(u.username, "tenant_abc_func");
  assertEquals(u.password, "hexpw");
  assertEquals(u.host, "db.internal:5432");
  assertEquals(u.pathname, "/eurobase");
  assertEquals(u.searchParams.get("sslmode"), "require");
});

function fakeFactory() {
  const created: string[] = [];
  const ended: string[] = [];
  const factory = (url: string) => {
    const user = new URL(url).username;
    created.push(user);
    return { user, end: () => { ended.push(user); return Promise.resolve(); } };
  };
  return { created, ended, factory };
}

Deno.test("concurrent first acquire shares one client", async () => {
  const f = fakeFactory();
  const pool = new TenantDBPool("postgres://r:p@h:5432/db", "s".repeat(32), f.factory, 5);
  const [a, b] = await Promise.all([pool.acquire("tenant_a", "tenant_a_func"), pool.acquire("tenant_a", "tenant_a_func")]);
  assert(a.client === b.client);
  assertEquals(f.created, ["tenant_a_func"]);
  a.release();
  b.release();
});

Deno.test("LRU evicts only idle entries; busy entries survive over the cap", async () => {
  let t = 0;
  const f = fakeFactory();
  const pool = new TenantDBPool("postgres://r:p@h:5432/db", "s".repeat(32), f.factory, 2, () => ++t);
  const a = await pool.acquire("tenant_a", "tenant_a_func"); // busy
  const b = await pool.acquire("tenant_b", "tenant_b_func");
  b.release(); // idle
  await pool.acquire("tenant_c", "tenant_c_func"); // over cap → evict idle b, not busy a
  await new Promise((r) => setTimeout(r, 0));
  assertEquals(f.ended, ["tenant_b_func"]);
  const d = await pool.acquire("tenant_d", "tenant_d_func"); // a and c busy → soft cap, nothing killed
  assertEquals(f.ended, ["tenant_b_func"]);
  assertEquals(pool.size, 3);
  a.release();
  d.release();
});

Deno.test("lease.invalidate drops its own client so the next acquire reconnects", async () => {
  const f = fakeFactory();
  const pool = new TenantDBPool("postgres://r:p@h:5432/db", "s".repeat(32), f.factory, 5);
  const a = await pool.acquire("tenant_a", "tenant_a_func");
  a.invalidate();
  a.release();
  const b = await pool.acquire("tenant_a", "tenant_a_func");
  assertEquals(f.created, ["tenant_a_func", "tenant_a_func"]);
  b.release();
});

Deno.test("a stale lease.invalidate does not evict a newer client", async () => {
  const f = fakeFactory();
  const pool = new TenantDBPool("postgres://r:p@h:5432/db", "s".repeat(32), f.factory, 5);
  const stale = await pool.acquire("tenant_a", "tenant_a_func");
  stale.invalidate(); // first failure drops client #1
  const fresh = await pool.acquire("tenant_a", "tenant_a_func"); // client #2
  stale.invalidate(); // late second failure on the old lease
  stale.release();
  const again = await pool.acquire("tenant_a", "tenant_a_func");
  assert(again.client === fresh.client);
  assertEquals(f.created.length, 2);
  fresh.release();
  again.release();
});

Deno.test("isLoginError", () => {
  assert(isLoginError({ code: "28P01" }));
  assert(isLoginError({ code: "28000" }));
  assert(isLoginError({ code: "53300" }));
  assert(isLoginError({ code: "08P01", message: "SASL authentication failed" }));
  assert(!isLoginError({ code: "08P01", message: "query_wait_timeout" }));
  assert(!isLoginError({ code: "42501" }));
  assert(!isLoginError(new Error("x")));
});

Deno.test("isPoolerBusyError", () => {
  assert(isPoolerBusyError({ code: "08P01", message: "query_wait_timeout" }));
  assert(isPoolerBusyError({ code: "08P01", message: "no more connections allowed (max_client_conn)" }));
  assert(!isPoolerBusyError({ code: "08P01", message: "SASL authentication failed" }));
  assert(!isPoolerBusyError({ code: "57014", message: "canceling statement due to statement timeout" }));
});

Deno.test("isConnectionDrop", () => {
  assert(isConnectionDrop({ code: "CONNECTION_CLOSED" }));
  assert(isConnectionDrop({ code: "ECONNRESET" }));
  // Draining PgBouncer refusing BEGIN (SIGINT safe shutdown).
  assert(isConnectionDrop({ code: "08P01", message: "server shutting down" }));
  assert(!isPoolerBusyError({ code: "08P01", message: "server shutting down" }));
  assert(!isLoginError({ code: "08P01", message: "server shutting down" }));
  assert(!isConnectionDrop({ code: "08P01" }));
  assert(!isConnectionDrop({ code: "42P01" }));
});

Deno.test("a dedicated target gets its own client, never the shared-cluster one (#676)", async () => {
  const urls: string[] = [];
  const factory = (url: string) => {
    urls.push(url);
    return { url, end: () => Promise.resolve() };
  };
  const pool = new TenantDBPool("postgres://r:p@shared:5432/eurobase", "s".repeat(32), factory, 5);
  const t1 = { id: "db-1", host: "ded1", port: 5432, database: "rdb" };
  const shared = await pool.acquire("tenant_a", "tenant_a_func");
  const ded = await pool.acquire("tenant_a", "tenant_a_func", t1);
  const ded2 = await pool.acquire("tenant_a", "tenant_a_func", { id: "db-2", host: "ded2", port: 5432, database: "rdb" });
  assert(shared.client !== ded.client);
  assert(ded.client !== ded2.client);
  assertEquals(urls.map((u) => new URL(u).hostname), ["shared", "ded1", "ded2"]);
  // A different password per instance (and not the shared cluster's).
  assertEquals(new Set(urls.map((u) => new URL(u).password)).size, 3);
  assertEquals(new URL(urls[1]).searchParams.get("sslmode"), "require");
  // invalidate drops only the dedicated entry.
  ded.invalidate();
  assertEquals(pool.size, 2);
  for (const l of [shared, ded, ded2]) l.release();
});
