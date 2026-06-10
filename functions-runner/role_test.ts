// Deno tests for the per-tenant role helpers. Run locally with:
//
//   cd functions-runner && deno test --allow-none role_test.ts
//
// CI doesn't currently run Deno tests; these are documentation +
// local-runnable assertions. The Go integration suite in
// internal/query/function_runner_role_test.go covers the end-to-end
// behavior against a real Postgres.

import { assertEquals, assertStrictEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { quoteIdent, rlsContextStatements, tenantFuncRole, validIdentRe } from "./role.ts";

Deno.test("tenantFuncRole appends _func suffix", () => {
  assertEquals(tenantFuncRole("tenant_abc"), "tenant_abc_func");
  assertEquals(tenantFuncRole("tenant_b24e9fa8_463f_452d_be4e_ee5127c3e8f7"), "tenant_b24e9fa8_463f_452d_be4e_ee5127c3e8f7_func");
});

Deno.test("validIdentRe accepts well-formed Postgres identifiers", () => {
  for (const ok of [
    "tenant_abc",
    "tenant_b24e9fa8_463f_452d_be4e_ee5127c3e8f7",
    "_underscore_first",
    "MixedCase123",
    "a", // single char, valid
  ]) {
    assertStrictEquals(validIdentRe.test(ok), true, `expected ${ok} to validate`);
  }
});

Deno.test("validIdentRe rejects SQL-injection shaped strings", () => {
  for (const bad of [
    "",
    "1starts_with_digit",
    "has space",
    "has-dash",
    "has.dot",
    "has;semicolon",
    'has"quote',
    "has'singlequote",
    "tenant_abc; DROP TABLE users",
    "tenant_abc' UNION SELECT",
    'tenant_abc"; SET ROLE postgres; --',
    "../../etc/passwd",
    "🦀",
    "x".repeat(64), // exceeds NAMEDATALEN-1 = 63
  ]) {
    assertStrictEquals(
      validIdentRe.test(bad),
      false,
      `expected ${JSON.stringify(bad)} to fail validation but it passed`,
    );
  }
});

Deno.test("quoteIdent wraps identifiers in double quotes", () => {
  assertEquals(quoteIdent("tenant_abc"), `"tenant_abc"`);
});

Deno.test("quoteIdent escapes embedded double quotes", () => {
  // This shouldn't happen if validIdentRe is checked first, but
  // quoteIdent's contract is that it produces a valid quoted identifier
  // for any input, even attacker-controlled.
  assertEquals(quoteIdent(`weird"name`), `"weird""name"`);
});

Deno.test("quoteIdent does NOT need escaping for normal underscore/digit identifiers", () => {
  // The role names we generate at runtime always pass validIdentRe so
  // never have quote characters. This is the common path.
  assertEquals(quoteIdent("tenant_b24e9fa8_func"), `"tenant_b24e9fa8_func"`);
});

Deno.test("rlsContextStatements with an end-user sets authenticated + end_user_id (issue #188)", () => {
  const stmts = rlsContextStatements("8f4a2c1e-0000-4000-8000-000000000001");
  assertEquals(stmts, [
    {
      sql: "SELECT set_config('app.end_user_id', $1, true)",
      params: ["8f4a2c1e-0000-4000-8000-000000000001"],
    },
    {
      sql: "SELECT set_config('app.end_user_role', 'authenticated', true)",
      params: [],
    },
  ]);
});

Deno.test("rlsContextStatements without an end-user sets the service role (issue #188)", () => {
  // No end-user JWT → tenant-deployed code runs as service, mirroring
  // gateway secret-key traffic, so is_service_role() is true and the
  // DDL presets' service branch applies.
  const stmts = rlsContextStatements("");
  assertEquals(stmts, [
    {
      sql: "SELECT set_config('app.end_user_role', 'service', true)",
      params: [],
    },
  ]);
});

Deno.test("rlsContextStatements never interpolates the user id into SQL text", () => {
  // The user id travels as a bind parameter; a hostile value can't
  // change the SQL shape.
  const stmts = rlsContextStatements("'; SET ROLE postgres; --");
  for (const s of stmts) {
    assertStrictEquals(s.sql.includes("postgres"), false);
  }
  assertEquals(stmts[0].params, ["'; SET ROLE postgres; --"]);
});

Deno.test("the full tenantFuncRole → quoteIdent pipeline produces safe SQL fragments", () => {
  const schema = "tenant_b24e9fa8_463f_452d_be4e_ee5127c3e8f7";
  const role = tenantFuncRole(schema);
  const setRoleSQL = `SET LOCAL ROLE ${quoteIdent(role)}`;
  const setPathSQL = `SET LOCAL search_path TO ${quoteIdent(schema)}`;

  // The two SQL statements should be parseable plain SQL with no
  // embedded statement-terminator or string-escape artefacts. We just
  // assert the shape rather than send to Postgres here.
  assertEquals(
    setRoleSQL,
    `SET LOCAL ROLE "tenant_b24e9fa8_463f_452d_be4e_ee5127c3e8f7_func"`,
  );
  assertEquals(
    setPathSQL,
    `SET LOCAL search_path TO "tenant_b24e9fa8_463f_452d_be4e_ee5127c3e8f7"`,
  );
});
