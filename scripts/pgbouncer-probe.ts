// postgres.js (the functions runner's driver) through PgBouncer, as the
// runner will use it (#641 PR 3). Run by scripts/test-pgbouncer.sh after
// the Go e2e test has provisioned a tenant with a login.
//
// Env: PGB_PROBE_GATEWAY (gateway via pooler), PGB_PROBE_TENANT_BASE
// (pooler URL with the tenant alias db; user/pass replaced), PGB_TEST_SECRET.
import { funcPassword, tenantDbUrl } from "../functions-runner/tenant_db.ts";
const { default: postgres } = await import("https://deno.land/x/postgresjs@v3.4.7/mod.js");

// A hang is a failure (postgres.js 3.4.4 never settled the waiting query).
setTimeout(() => { console.log("FAIL probe hung (watchdog)"); Deno.exit(1); }, 30000);
// Mirror the runner's guard (server.ts): log, don't crash.
globalThis.addEventListener("unhandledrejection", (e) => {
  e.preventDefault();
  console.log(`INFO unhandled rejection (runner logs + survives): ${(e.reason as any)?.code} ${(e.reason as Error)?.message}`);
});
let failed = false;
const fail = (m: string) => { console.log("FAIL " + m); failed = true; };
const ok = (m: string) => console.log("OK   " + m);

const gw = postgres(Deno.env.get("PGB_PROBE_GATEWAY")!, { max: 1 });
const [t] = await gw`SELECT n.nspname AS s FROM pg_namespace n JOIN pg_roles r ON r.rolname = n.nspname || '_func' AND r.rolcanlogin WHERE n.nspname ~ '^tenant_[0-9a-f_]+$' ORDER BY 1 LIMIT 1`;
await gw.end();
if (!t) { fail("no loginable tenant (run the Go e2e test first)"); Deno.exit(1); }
const base = Deno.env.get("PGB_PROBE_TENANT_BASE")!;
const secret = Deno.env.get("PGB_TEST_SECRET")!;
const url = tenantDbUrl(base, t.s + "_func", await funcPassword(secret, t.s));

// Same client options and transaction shape as runDBSql in server.ts.
const a = postgres(url, { max: 1, idle_timeout: 30 });
try {
  for (let i = 0; i < 30; i++) {
    const r = await a.begin(async (tx: any) => {
      await tx.unsafe("SET LOCAL statement_timeout = 5000");
      await tx.unsafe(`SET LOCAL search_path TO "${t.s}"`);
      await tx.unsafe("SET LOCAL standard_conforming_strings = on");
      return await tx.unsafe("SELECT $1::int + 1 AS n, session_user AS u", [i]);
    });
    if (r[0].n !== i + 1 || r[0].u !== t.s + "_func") throw new Error(`bad row ${JSON.stringify(r[0])}`);
  }
  ok("30 transactions with parameters (prepared statements) as " + t.s.slice(0, 15) + "…");
} catch (e) { fail("prepared statements: " + (e as Error).message); }

// Wrong password: the code the runner maps with isLoginError.
const bad = postgres(tenantDbUrl(base, t.s + "_func", "wrong"), { max: 1 });
try { await bad`SELECT 1`; fail("wrong password accepted"); }
catch (e) { console.log(`INFO wrong password -> code=${(e as any).code} msg=${(e as Error).message}`); }
await bad.end();

// Tenant pool exhausted (test runs tenant pool 1, query_wait_timeout 2 s):
// one client holds the only server connection; the other must get a
// 08P01 query_wait_timeout it can catch — not hang (runner maps it to a
// retryable "busy" error via isPoolerBusyError).
const b = postgres(url, { max: 1 });
const hold = a.begin(async (tx: any) => { await tx.unsafe("SELECT pg_sleep(4)"); });
await new Promise((r) => setTimeout(r, 300));
try { await b`SELECT 1`; fail("second client got a connection; pool not exhausted?"); }
catch (e) {
  const code = (e as any).code, msg = (e as Error).message;
  if (code === "08P01" && /query_wait_timeout/.test(msg)) ok("pool exhausted -> catchable 08P01 query_wait_timeout");
  else fail(`pool exhausted -> unexpected ${code} ${msg}`);
}
await hold.catch(() => {});
// The client recovers once the pool frees up.
try { await b`SELECT 1`; ok("client recovers after the pool frees up"); }
catch (e) { fail("client did not recover: " + (e as Error).message); }
await a.end(); await b.end();
Deno.exit(failed ? 1 : 0);
