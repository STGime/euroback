import { assertEquals, assertStrictEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { checkAsServiceQuery } from "./asservice_guard.ts";

Deno.test("regular ctx.db.sql (mode undefined) is never gated by this helper", () => {
  // Even with allow_service_role=false and a platform-table
  // reference, a non-asService call passes the gate — RLS is the
  // control on that path, not this helper.
  assertStrictEquals(
    checkAsServiceQuery(undefined, false, "SELECT * FROM users"),
    null,
  );
});

Deno.test("mode=service ✕ allow_service_role=false → rejects with a caller-actionable error", () => {
  const msg = checkAsServiceQuery("service", false, "SELECT 1");
  assertEquals(
    msg,
    "ctx.db.asService() is not enabled for this function — set allow_service_role=true on the function to opt in",
  );
});

Deno.test("mode=service ✕ allow_service_role=true ✕ safe query → allowed", () => {
  assertStrictEquals(
    checkAsServiceQuery(
      "service",
      true,
      "INSERT INTO posts (title, body, approved) VALUES ($1, $2, false) RETURNING id",
    ),
    null,
  );
});

Deno.test("mode=service ✕ allow_service_role=true ✕ platform table → rejected", () => {
  // The primary auth-table exposure the review flagged: a JWT-verified
  // function with allow_service_role could otherwise read password
  // hashes because is_service_role()-gated RLS policies open up under
  // asService. This gate closes that.
  assertEquals(
    checkAsServiceQuery(
      "service",
      true,
      "SELECT id, email, password_hash FROM users",
    ),
    'ctx.db.asService() cannot reference the platform system table "users" — use the platform APIs (e.g. auth flows) for that surface',
  );
});

Deno.test("mode=service refresh_tokens INSERT rejected (session-forgery vector)", () => {
  assertEquals(
    checkAsServiceQuery(
      "service",
      true,
      "INSERT INTO refresh_tokens (user_id, token_hash) VALUES ($1, $2)",
    ),
    'ctx.db.asService() cannot reference the platform system table "refresh_tokens" — use the platform APIs (e.g. auth flows) for that surface',
  );
});

Deno.test("opt-in gate fires BEFORE the platform-table fence (message names the opt-in, not the table)", () => {
  // Order matters for developer experience: a tenant who hasn't
  // enabled the flag should see the actionable "flip the flag" error,
  // not the more specific table-name error. Fixing the opt-in first
  // usually resolves the second.
  const msg = checkAsServiceQuery("service", false, "SELECT * FROM users");
  assertStrictEquals(msg?.includes("allow_service_role=true"), true);
  assertStrictEquals(msg?.includes("platform system table"), false);
});

Deno.test("platform-table fence is case-insensitive (Postgres folds unquoted idents)", () => {
  assertStrictEquals(
    checkAsServiceQuery("service", true, "SELECT * FROM Users")?.includes(
      '"users"',
    ),
    true,
  );
  assertStrictEquals(
    checkAsServiceQuery("service", true, 'DELETE FROM "USERS"')?.includes(
      '"users"',
    ),
    true,
  );
});
