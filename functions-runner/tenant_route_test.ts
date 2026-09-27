import { assertEquals, assertRejects } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { dedicatedDbUrl, RouteCache, routeFromRows, targetKey, TenantDbNotReady } from "./tenant_route.ts";

const active = { host: "10.0.0.5", port: 5432, database_name: "rdb", state: "active" };

Deno.test("routeFromRows: no project_databases row → shared cluster", () => {
  assertEquals(routeFromRows([]), { kind: "shared" });
  assertEquals(routeFromRows(undefined), { kind: "shared" });
});

Deno.test("routeFromRows: active row → dedicated target", () => {
  assertEquals(routeFromRows([active]), {
    kind: "dedicated",
    target: { host: "10.0.0.5", port: 5432, database: "rdb" },
  });
});

Deno.test("routeFromRows: live but not active → not_ready (never shared)", () => {
  for (const state of ["provisioning", "restoring"]) {
    assertEquals(routeFromRows([{ ...active, state }]), { kind: "not_ready", state });
  }
  // Active but no endpoint yet is not routable either.
  assertEquals(routeFromRows([{ ...active, host: null }]), { kind: "not_ready", state: "active" });
});

Deno.test("dedicatedDbUrl: tenant role, TLS required, escaped credentials", () => {
  const u = new URL(dedicatedDbUrl({ host: "10.0.0.5", port: 6000, database: "rdb" }, "tenant_a_func", "p@ss/w"));
  assertEquals(u.hostname, "10.0.0.5");
  assertEquals(u.port, "6000");
  assertEquals(u.pathname, "/rdb");
  assertEquals(decodeURIComponent(u.username), "tenant_a_func");
  assertEquals(decodeURIComponent(u.password), "p@ss/w");
  assertEquals(u.searchParams.get("sslmode"), "require");
});

Deno.test("targetKey differs per instance", () => {
  const a = targetKey({ host: "h1", port: 5432, database: "rdb" });
  const b = targetKey({ host: "h2", port: 5432, database: "rdb" });
  assertEquals(a === b, false);
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
