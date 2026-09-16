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

// --- Second-review bypass hardening ---
//
// The following tests were added after the reviewer showed the
// lexical table-name scan alone could be bypassed via procedural
// bodies and Unicode-escaped identifiers. Each test corresponds to a
// concrete PG16 exploit the reviewer demonstrated on real 000050
// auth policies; the assertion is that checkAsServiceQuery refuses
// the query before it reaches the DB.

Deno.test("DO $$ ... $$ body is refused (bypass: account takeover via procedural block)", () => {
  // The scanner deliberately skips dollar-quoted bodies (mirrors the
  // Go SDK scanner). A DO block runs as the current role
  // (<schema>_func — which has DML on users via 000047) with the
  // session GUC still 'service', so without this refusal an
  // asService caller could UPDATE users SET password_hash = ...
  // for any tenant end-user.
  const msg = checkAsServiceQuery(
    "service",
    true,
    "DO $$ BEGIN UPDATE users SET password_hash='pwned' WHERE email='victim@x'; END $$",
  );
  assertStrictEquals(
    msg?.includes("SELECT/INSERT/UPDATE/DELETE/WITH statements (got DO)"),
    true,
    `expected first-keyword rejection, got: ${msg}`,
  );
});

Deno.test("CREATE / PREPARE / EXECUTE / DECLARE / COPY / SET are all refused (statement-kind allowlist)", () => {
  for (const kind of ["CREATE", "PREPARE", "EXECUTE", "DECLARE", "COPY", "SET", "RESET", "BEGIN", "TABLE", "VALUES"]) {
    const msg = checkAsServiceQuery("service", true, kind + " something");
    assertStrictEquals(
      msg !== null && msg.includes(`got ${kind}`),
      true,
      `expected ${kind} to be refused, got: ${msg}`,
    );
  }
});

Deno.test("dollar-quoted body inside an otherwise-DML query is refused (defence-in-depth)", () => {
  // Belt-and-braces against a future statement kind that legitimately
  // takes a dollar-body slipping through the first-keyword check.
  const msg = checkAsServiceQuery(
    "service",
    true,
    "INSERT INTO posts (body) VALUES ($$ hostile literal $$) RETURNING id",
  );
  assertStrictEquals(
    msg?.includes("dollar-quoted bodies"),
    true,
    `expected dollar-body rejection, got: ${msg}`,
  );
});

Deno.test("dollar-body regex does NOT false-positive on $1/$2 parameter placeholders", () => {
  // Parameterised queries must keep working — the whole point of
  // asService() is server-approved DML that binds user input as $1,
  // $2. $N is a numbered placeholder, not a dollar-body opener.
  assertStrictEquals(
    checkAsServiceQuery(
      "service",
      true,
      "INSERT INTO posts (author_id, title) VALUES ($1, $2) RETURNING id",
    ),
    null,
  );
});

Deno.test('U&"\\0075sers" Unicode-escaped identifier is refused (bypass: encodes "users" past the scanner)', () => {
  // The scanner treats U and sers as separate idents (dot-joined into
  // three tokens) and never sees "users" as a whole word. Since PG
  // parses U&"\0075sers" as the identifier "users", the fence must
  // refuse the U& syntax itself.
  const msg = checkAsServiceQuery(
    "service",
    true,
    'SELECT email||\'=\'||password_hash FROM U&"\\0075sers"',
  );
  assertStrictEquals(
    msg?.includes("Unicode-escaped"),
    true,
    `expected U-escape rejection, got: ${msg}`,
  );
});

Deno.test("U&'string' form is also refused (intent bypass via string-literal path)", () => {
  // The identifier form is what encodes "users"; the string-literal
  // form is unlikely to be needed and blocking both removes any
  // U& codepath from the surface.
  const msg = checkAsServiceQuery(
    "service",
    true,
    "SELECT U&'\\0075sers'",
  );
  assertStrictEquals(
    msg?.includes("Unicode-escaped"),
    true,
    `expected U-escape rejection, got: ${msg}`,
  );
});

Deno.test("CTE-DML (WITH x AS (...) INSERT INTO posts SELECT * FROM x) keeps working", () => {
  // Reviewer explicitly asked to verify this positive case survives
  // the tightened rules — CTE-wrapped DML is the natural shape for
  // "compute an approval decision, then insert" flows.
  assertStrictEquals(
    checkAsServiceQuery(
      "service",
      true,
      "WITH approved AS (SELECT $1::text AS title, $2::text AS body) " +
        "INSERT INTO posts (title, body, approved) " +
        "SELECT title, body, false FROM approved RETURNING id",
    ),
    null,
  );
});

Deno.test("opt-in check still fires before the new statement-kind check", () => {
  // The reviewer explicitly asked to preserve this ordering: a
  // tenant who hasn't enabled the flag should see the actionable
  // "flip the flag" error, not the "wrong statement kind" one, so
  // fixing the opt-in usually resolves the second question too.
  const msg = checkAsServiceQuery("service", false, "DO $$ SELECT 1 $$");
  assertStrictEquals(msg?.includes("allow_service_role=true"), true);
  assertStrictEquals(msg?.includes("SELECT/INSERT/UPDATE/DELETE/WITH"), false);
});
