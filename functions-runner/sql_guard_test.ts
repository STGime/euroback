import { assert, assertEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { privilegeStatementError, SECURITY_DEFINER_REJECTION, uuidRe } from "./sql_guard.ts";

Deno.test("privilegeStatementError blocks role/session/privilege statements", () => {
  const blocked = [
    "RESET ROLE",
    "reset role; select 1",
    "SET ROLE eurobase_function_runner",
    "set local role x",
    "SET SESSION AUTHORIZATION x",
    "RESET ALL",
    "SET search_path TO public",
    "select set_config('role', 'x', true)",
    "GRANT SELECT ON t TO x",
    "REVOKE ALL ON t FROM x",
    "ALTER DEFAULT PRIVILEGES GRANT SELECT ON TABLES TO x",
    "CREATE ROLE x",
    "ALTER TABLE t OWNER TO x",
    "CREATE FUNCTION f() RETURNS int LANGUAGE sql SECURITY DEFINER AS $$ select 1 $$",
    // review follow-ups
    "SELECT E'\\'', set_config('x','y',true)",
    "SELECT E'\\''; RESET ROLE; SELECT 'x'",
    "SELECT 1 AS ä$x$; RESET ROLE; SELECT 1 AS ö$x$",
    'SET "role" TO \'x\'',
    'RESET "role"',
    'SET "search_path" TO x',
    'SET U&"search\\005fpath" TO x',
    'SELECT "set_config"(\'a\',\'b\',true)',
    "SET session_authorization = 'x'",
    "CREATE SCHEMA s AUTHORIZATION x",
    "UPDATE pg_settings SET setting = 'x' WHERE name = 'y'",
    "DO $$ BEGIN PERFORM 1; END $$",
    "CREATE OR REPLACE FUNCTION f() RETURNS int LANGUAGE sql AS $$ select 1 $$",
    "create procedure p() language sql as $$ select 1 $$",
    "COMMIT",
    "select 1; commit; select 2",
    "ROLLBACK",
    "SAVEPOINT a",
    "BEGIN",
    "START TRANSACTION",
    "END",
  ];
  for (const q of blocked) assert(privilegeStatementError(q) !== null, `expected block: ${q}`);
});

Deno.test("privilegeStatementError allows normal tenant SQL", () => {
  const allowed = [
    "SELECT * FROM users WHERE role = $1",
    "SELECT 'reset role' AS s",
    "-- set role x\nSELECT 1",
    "/* grant */ SELECT 1",
    'SELECT "grant", "role" FROM t',
    "INSERT INTO posts (owner, title) VALUES ($1, $2)",
    "SELECT $tag$ RESET ROLE $tag$",
    "UPDATE settings SET value = $1 WHERE key = 'search'",
    "UPDATE t SET role = $1 WHERE id = $2",
    "INSERT INTO m (id, role) VALUES ($1, $2) ON CONFLICT (id) DO UPDATE SET role = EXCLUDED.role",
    "SELECT E'it\\'s fine' AS s",
    "SELECT col$1 FROM t",
    "UPDATE t SET ärole = 1",
    "SELECT CASE WHEN a THEN 1 ELSE 0 END FROM t",
  ];
  for (const q of allowed) assertEquals(privilegeStatementError(q), null, q);
});

Deno.test("uuidRe", () => {
  assert(uuidRe.test("2c8a49f2-3970-485f-83a8-336ef87b9fe0"));
  assert(!uuidRe.test("not-a-uuid"));
  assert(!uuidRe.test("2c8a49f2-3970-485f-83a8-336ef87b9fe0' OR 1=1"));
});

Deno.test("privilegeStatementError refuses string-literal settings", () => {
  for (const s of [
    "SET standard_conforming_strings = off",
    "SET LOCAL backslash_quote = on",
    'SET "standard_conforming_strings" = off',
    "RESET escape_string_warning",
  ]) {
    assert(privilegeStatementError(s) !== null, s);
  }
  assertEquals(privilegeStatementError("SELECT 'standard_conforming_strings' AS doc"), null);
});

// #661: SECURITY DEFINER is explained, and the text matches the gateway's
// query.SecurityDefinerRejection (privilege_guard.go) word for word.
Deno.test("privilegeStatementError explains SECURITY DEFINER", async () => {
  assertEquals(privilegeStatementError("ALTER FUNCTION f() SECURITY DEFINER"), SECURITY_DEFINER_REJECTION);
  const goSrc = await Deno.readTextFile(new URL("../internal/query/privilege_guard.go", import.meta.url));
  const start = goSrc.indexOf("const SecurityDefinerRejection =");
  const end = goSrc.indexOf("SecurityDefinerDocsURL", start + 1);
  const literals = [...goSrc.slice(start, end).matchAll(/"((?:[^"\\]|\\.)*)"/g)].map((m) => m[1].replaceAll('\\"', '"'));
  const url = /SecurityDefinerDocsURL = "([^"]+)"/.exec(goSrc)![1];
  assertEquals(literals.join("") + url, SECURITY_DEFINER_REJECTION);
});
