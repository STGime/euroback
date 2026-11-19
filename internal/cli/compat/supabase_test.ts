// Tests for the Supabase compatibility layer:
//
//   deno test --no-check internal/cli/compat/
//
// They check the SQL each call runs and the shape of what comes back,
// against a recorded ctx. The end-to-end check with a real database is
// function_supabase_compat in internal/gateway/team_e2e_test.go.

import { assert, assertEquals, assertRejects, assertThrows } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { ANON_KEY, createClient, parseColumns, SERVICE_ROLE_KEY, toParam } from "./supabase.ts";

type Call = { query: string; params: unknown[]; service: boolean };

function withCtx(opts: { user?: { id: string; email: string } | null; reply?: (c: Call) => unknown[] } = {}) {
  const calls: Call[] = [];
  const reply = opts.reply ?? (() => [{ data: [] }]);
  const record = (service: boolean) => (query: string, params: unknown[] = []) => {
    const c = { query, params, service };
    calls.push(c);
    try {
      return Promise.resolve(reply(c));
    } catch (e) {
      return Promise.reject(e);
    }
  };
  const storage: Call[] = [];
  (globalThis as any).__eurobase = {
    ctx: {
      user: opts.user === undefined ? { id: "u-1", email: "u1@example.org" } : opts.user,
      db: { sql: record(false), asService: () => ({ sql: record(true) }) },
      storage: {
        upload: (key: string, _b: unknown, o: unknown) => { storage.push({ query: "upload " + key, params: [o], service: false }); return Promise.resolve({ key }); },
        createSignedUrl: (key: string, op: string) => { storage.push({ query: `${op} ${key}`, params: [], service: false }); return Promise.resolve({ url: "https://files.test/" + key }); },
        delete: (key: string) => { storage.push({ query: "delete " + key, params: [], service: false }); return Promise.resolve({}); },
      },
    },
  };
  return { calls, storage };
}

Deno.test("select: columns, filters, order, limit become one statement", async () => {
  const { calls } = withCtx({ reply: () => [{ data: [{ id: 1, name: "a" }] }] });
  const sb = createClient("x", ANON_KEY);
  const { data, error } = await sb.from("orders").select("id, name, total:amount").eq("user_id", "u-1").gt("amount", 5).is("deleted_at", null)
    .in("status", ["open", "paid"]).order("created_at", { ascending: false }).limit(10);
  assertEquals(error, null);
  assertEquals(data, [{ id: 1, name: "a" }]);
  assertEquals(calls.length, 1);
  assertEquals(
    calls[0].query,
    `SELECT coalesce(json_agg(_r), '[]'::json) AS data FROM (SELECT "id", "name", "amount" AS "total" FROM "orders" WHERE "orders"."user_id" = $1 AND "orders"."amount" > $2 AND "orders"."deleted_at" IS NULL AND "orders"."status" IN ($3, $4) ORDER BY "orders"."created_at" DESC LIMIT 10) _r`,
  );
  assertEquals(calls[0].params, ["u-1", 5, "open", "paid"]);
  assertEquals(calls[0].service, false);
});

Deno.test("values never reach the SQL text", async () => {
  const { calls } = withCtx();
  const evil = `x'; DROP TABLE users; --`;
  await createClient("x", ANON_KEY).from("t").select("*").eq("name", evil).like("note", evil);
  assert(!calls[0].query.includes("DROP"));
  assertEquals(calls[0].params, [evil, evil]);
});

Deno.test("names are quoted; anything but a plain column name is refused", () => {
  withCtx();
  const sb = createClient("x", ANON_KEY);
  assertEquals(sb.from(`we"ird`).select("*").toSQL().text.includes(`FROM "we""ird"`), true);
  assertThrows(() => sb.from("t").select("*").eq(`a" = 1 OR "b`, 1), Error, "plain column names");
  assertThrows(() => parseColumns(`id, "x"; DROP TABLE t`), Error);
  assertThrows(() => parseColumns("*, profiles(*)"), Error, "embedded table");
  assertEquals(parseColumns("id, created_at::text"), `"id", "created_at"::text AS "created_at"`);
});

Deno.test("single / maybeSingle", async () => {
  withCtx({ reply: () => [{ data: [{ id: 1 }] }] });
  assertEquals((await createClient("x", ANON_KEY).from("t").select("*").eq("id", 1).single()).data, { id: 1 });
  withCtx({ reply: () => [{ data: [] }] });
  const none = await createClient("x", ANON_KEY).from("t").select("*").eq("id", 1).single();
  assertEquals(none.data, null);
  assertEquals(none.error.code, "PGRST116");
  const maybe = await createClient("x", ANON_KEY).from("t").select("*").eq("id", 1).maybeSingle();
  assertEquals([maybe.data, maybe.error], [null, null]);
});

Deno.test("insert: rows travel as one JSON parameter; data only with select()", async () => {
  const { calls } = withCtx({ reply: () => [{ data: [{ id: 7 }] }] });
  const sb = createClient("x", ANON_KEY);
  const plain = await sb.from("purchases").insert({ user_id: "u-1", tags: ["a", "b"], meta: { k: 1 } });
  assertEquals(plain.data, null);
  assertEquals(calls[0].query, `INSERT INTO "purchases" ("user_id", "tags", "meta") SELECT "user_id", "tags", "meta" FROM json_populate_recordset(null::"purchases", $1::text::json)`);
  assertEquals(calls[0].params, [JSON.stringify([{ user_id: "u-1", tags: ["a", "b"], meta: { k: 1 } }])]);
  const back = await sb.from("purchases").insert([{ a: 1 }, { b: 2 }]).select("id").single();
  assertEquals(back.data, { id: 7 });
  assertEquals(calls[1].query.startsWith(`WITH _w AS (INSERT INTO "purchases" ("a", "b") SELECT "a", "b" FROM json_populate_recordset(null::"purchases", $1::text::json) RETURNING "purchases".*) SELECT`), true);
});

Deno.test("upsert: conflict target and updated columns", () => {
  withCtx();
  const sb = createClient("x", ANON_KEY);
  assertEquals(
    sb.from("subs").upsert({ user_id: "u", plan: "pro" }, { onConflict: "user_id" }).toSQL().text,
    `INSERT INTO "subs" ("user_id", "plan") SELECT "user_id", "plan" FROM json_populate_recordset(null::"subs", $1::text::json) ON CONFLICT ("user_id") DO UPDATE SET "user_id" = EXCLUDED."user_id", "plan" = EXCLUDED."plan"`,
  );
  assert(sb.from("subs").upsert({ id: 1, plan: "pro" }).toSQL().text.includes(`ON CONFLICT ("id") DO UPDATE`));
  assert(sb.from("subs").upsert({ id: 1 }, { ignoreDuplicates: true }).toSQL().text.endsWith(`ON CONFLICT ("id") DO NOTHING`));
  assertThrows(() => sb.from("subs").upsert({ plan: "pro" }).toSQL(), Error, "onConflict");
});

Deno.test("update and delete need a filter", async () => {
  const { calls } = withCtx();
  const sb = createClient("x", ANON_KEY);
  await sb.from("subs").update({ plan: "free", ends_at: null }).eq("user_id", "u-1");
  assertEquals(calls[0].query, `UPDATE "subs" SET "plan" = _v."plan", "ends_at" = _v."ends_at" FROM json_populate_record(null::"subs", $2::text::json) _v WHERE "subs"."user_id" = $1`);
  assertEquals(calls[0].params, ["u-1", JSON.stringify({ plan: "free", ends_at: null })]);
  // Only the table's own columns come back, not the joined values row.
  assert(sb.from("subs").update({ plan: "x" }).eq("id", 1).select().toSQL().text.includes(`_v WHERE "subs"."id" = $1 RETURNING "subs".*)`));
  await sb.from("subs").delete().eq("id", 3).select();
  assert(calls[1].query.startsWith(`WITH _w AS (DELETE FROM "subs" WHERE "subs"."id" = $1 RETURNING "subs".*)`));
  assert((await sb.from("subs").update({ plan: "x" })).error.message.includes("needs a filter"));
  assert((await sb.from("subs").delete()).error.message.includes("needs a filter"));
  await assertRejects(() => Promise.resolve(sb.from("subs").delete().throwOnError()), Error, "needs a filter");
});

Deno.test("a database error comes back as error, not as a throw", async () => {
  withCtx({ reply: () => { throw new Error("permission denied for table subs"); } });
  const r = await createClient("x", ANON_KEY).from("subs").select("*");
  assertEquals(r.data, null);
  assertEquals(r.error.message, "permission denied for table subs");
});

Deno.test("the service role key runs as the service role; any other key as the caller", async () => {
  let ctx = withCtx();
  await createClient("x", SERVICE_ROLE_KEY).from("t").select("*");
  await createClient("x", ANON_KEY).from("t").select("*");
  await createClient("x", "some-other-key").from("t").select("*");
  assertEquals(ctx.calls.map((c) => c.service), [true, false, false]);
  // No signed-in caller: the function is the service role already, and
  // asService() is not needed.
  ctx = withCtx({ user: null });
  await createClient("x", SERVICE_ROLE_KEY).from("t").select("*");
  assertEquals(ctx.calls.map((c) => c.service), [false]);
});

Deno.test("auth.getUser answers from the verified caller", async () => {
  withCtx({ reply: () => [{ data: { email: "u1@example.org", phone: null, metadata: { name: "U", _app_metadata: { plan: "pro" } }, created_at: "2025-01-01T00:00:00+00:00" } }] });
  const { data, error } = await createClient("x", ANON_KEY).auth.getUser("whatever-token");
  assertEquals(error, null);
  assertEquals([data.user.id, data.user.email, data.user.user_metadata, data.user.app_metadata], ["u-1", "u1@example.org", { name: "U" }, { plan: "pro" }]);
  withCtx({ user: null });
  const anon = await createClient("x", ANON_KEY).auth.getUser("whatever-token");
  assertEquals(anon.data.user, null);
  assertEquals(anon.error.status, 401);
});

Deno.test("rpc: named arguments; one plain value or rows", async () => {
  let ctx = withCtx({ reply: () => [{ data: 42 }] });
  const one = await createClient("x", ANON_KEY).rpc("credit_balance", { p_user: "u-1" });
  assertEquals(one.data, 42);
  assertEquals(ctx.calls[0].query, `SELECT to_json(_r) AS data FROM "credit_balance"("p_user" => $1) _r`);
  ctx = withCtx({ reply: () => [{ data: { id: 1 } }, { data: { id: 2 } }] });
  assertEquals((await createClient("x", ANON_KEY).rpc("team_roster")).data, [{ id: 1 }, { id: 2 }]);
});

Deno.test("storage: a bucket is a top folder", async () => {
  const { storage } = withCtx();
  const bucket = createClient("x", ANON_KEY).storage.from("exports");
  assertEquals((await bucket.upload("u-1/a.pdf", new Uint8Array([1]), { contentType: "application/pdf" })).data, { path: "u-1/a.pdf", fullPath: "exports/u-1/a.pdf" });
  assertEquals((await bucket.createSignedUrl("u-1/a.pdf", 60)).data, { signedUrl: "https://files.test/exports/u-1/a.pdf" });
  await bucket.remove(["u-1/a.pdf"]);
  assertEquals(storage.map((c) => c.query), ["upload exports/u-1/a.pdf", "download exports/u-1/a.pdf", "delete exports/u-1/a.pdf"]);
});

Deno.test("what isn't supported says so", () => {
  withCtx();
  const sb = createClient("x", ANON_KEY);
  assertThrows(() => sb.functions.invoke("other"), Error, "not available on Eurobase");
  assertThrows(() => sb.channel("room"), Error, "realtime");
  assertThrows(() => sb.from("t").select("*").or("a.eq.1,b.eq.2"), Error, "or(");
  assertThrows(() => sb.auth.admin.listUsers(), Error, "auth.admin.listUsers");
  assertThrows(() => sb.storage.from("b").getPublicUrl("x"), Error, "getPublicUrl");
});

Deno.test("toParam leaves values for the driver to write in the column's type", () => {
  assertEquals([toParam(null), toParam(undefined), toParam(5), toParam(true), toParam("s"), toParam({ a: 1 }), toParam([1, 2]), toParam(10n)], [null, null, 5, true, "s", { a: 1 }, [1, 2], "10"]);
});

Deno.test("outside a request the client refuses", async () => {
  (globalThis as any).__eurobase = undefined;
  const r = await createClient("x", ANON_KEY).from("t").select("*");
  assert(r.error.message.includes("no request is being handled"));
});
