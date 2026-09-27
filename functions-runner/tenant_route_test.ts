import { assertEquals, assertRejects } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { funcPassword } from "./tenant_db.ts";
import { dedicatedDbUrl, dedicatedSubject, RouteCache, routeFromRows, targetKey, TenantDbNotReady } from "./tenant_route.ts";

const DBID = "11111111-2222-3333-4444-555555555555";
const active = { id: DBID, host: "10.0.0.5", port: 5432, database_name: "rdb", state: "active" };

Deno.test("routeFromRows: no project_databases row → shared cluster", () => {
  assertEquals(routeFromRows([]), { kind: "shared" });
  assertEquals(routeFromRows(undefined), { kind: "shared" });
});

Deno.test("routeFromRows: active row → dedicated target", () => {
  assertEquals(routeFromRows([active]), {
    kind: "dedicated",
    target: { id: DBID, host: "10.0.0.5", port: 5432, database: "rdb" },
  });
});

Deno.test("routeFromRows: live but not active → not_ready (never shared)", () => {
  for (const state of ["provisioning", "restoring"]) {
    assertEquals(routeFromRows([{ ...active, state }]), { kind: "not_ready", state });
  }
  // Active but no endpoint yet is not routable either.
  assertEquals(routeFromRows([{ ...active, host: null }]), { kind: "not_ready", state: "active" });
  // In-flight moves reported by runner_get_tenant_db (no host).
  for (const state of ["upgrading", "maintenance"]) {
    assertEquals(routeFromRows([{ id: null, host: null, port: null, database_name: null, state }]), { kind: "not_ready", state });
  }
});

Deno.test("dedicatedDbUrl: tenant role, TLS required, escaped credentials", () => {
  const u = new URL(dedicatedDbUrl({ id: DBID, host: "10.0.0.5", port: 6000, database: "rdb" }, "tenant_a_func", "p@ss/w"));
  assertEquals(u.hostname, "10.0.0.5");
  assertEquals(u.port, "6000");
  assertEquals(u.pathname, "/rdb");
  assertEquals(decodeURIComponent(u.username), "tenant_a_func");
  assertEquals(decodeURIComponent(u.password), "p@ss/w");
  assertEquals(u.searchParams.get("sslmode"), "require");
});

Deno.test("targetKey differs per instance", () => {
  const a = targetKey({ id: DBID, host: "h1", port: 5432, database: "rdb" });
  const b = targetKey({ id: DBID, host: "h2", port: 5432, database: "rdb" });
  const c = targetKey({ id: "other", host: "h1", port: 5432, database: "rdb" });
  assertEquals(new Set([a, b, c]).size, 3);
});

Deno.test("RouteCache caches routes for the TTL, but not not_ready", async () => {
  let now = 0;
  const cache = new RouteCache(30_000, () => now);
  let calls = 0;
  let rows = [{ ...active, state: "provisioning" }];
  const lookup = () => {
    calls++;
    return Promise.resolve(rows);
  };
  assertEquals((await cache.get("p", lookup)).kind, "not_ready");
  rows = [active];
  assertEquals((await cache.get("p", lookup)).kind, "dedicated"); // not_ready wasn't cached
  assertEquals((await cache.get("p", lookup)).kind, "dedicated");
  assertEquals(calls, 2);
  now = 30_001;
  await cache.get("p", lookup);
  assertEquals(calls, 3);
});

Deno.test("RouteCache propagates a failed lookup (no shared fallback)", async () => {
  const cache = new RouteCache();
  await assertRejects(() => cache.get("p", () => Promise.reject(new Error("boom"))), Error, "boom");
});

Deno.test("TenantDbNotReady carries a retryable message", () => {
  const e = new TenantDbNotReady("provisioning");
  assertEquals(e.message.includes("retry shortly"), true);
});

Deno.test("dedicated password matches the Go vector (tenantlogin.DedicatedSubject)", async () => {
  const subject = dedicatedSubject({ id: DBID, host: "h", port: 5432, database: "rdb" }, "tenant_abc");
  assertEquals(subject, "ded:" + DBID + ":tenant_abc");
  assertEquals(
    await funcPassword("0123456789abcdef0123456789abcdef", subject),
    "c6434a60fd4e27d7e05724754c2268a21c5577312cc743855b31a116822948bb",
  );
});
