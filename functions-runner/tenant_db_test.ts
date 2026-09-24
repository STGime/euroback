import { assert, assertEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { funcPassword, isAuthError, TenantDBPool, tenantDbUrl } from "./tenant_db.ts";

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

Deno.test("TenantDBPool reuses per tenant and evicts LRU at the cap", async () => {
  const created: string[] = [];
  const ended: string[] = [];
  const factory = (url: string) => {
    const user = new URL(url).username;
    created.push(user);
    return { end: () => { ended.push(user); return Promise.resolve(); } };
  };
  const pool = new TenantDBPool("postgres://r:p@h:5432/db", "s".repeat(32), factory, 2);
  const a1 = await pool.get("tenant_a", "tenant_a_func");
  const a2 = await pool.get("tenant_a", "tenant_a_func");
  assert(a1 === a2, "same tenant reuses its client");
  await new Promise((r) => setTimeout(r, 2));
  await pool.get("tenant_b", "tenant_b_func");
  await new Promise((r) => setTimeout(r, 2));
  await pool.get("tenant_a", "tenant_a_func"); // touch a → b is now LRU
  await pool.get("tenant_c", "tenant_c_func"); // over cap → evict b
  assertEquals(pool.size, 2);
  assertEquals(ended, ["tenant_b_func"]);
  assertEquals(created, ["tenant_a_func", "tenant_b_func", "tenant_c_func"]);
});

Deno.test("isAuthError", () => {
  assert(isAuthError({ code: "28P01" }));
  assert(isAuthError({ code: "28000" }));
  assert(!isAuthError({ code: "42501" }));
  assert(!isAuthError(new Error("x")));
});
