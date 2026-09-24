import { assert, assertEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { privilegeStatementError, uuidRe } from "./sql_guard.ts";

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
  ];
  for (const q of allowed) assertEquals(privilegeStatementError(q), null, q);
});

Deno.test("uuidRe", () => {
  assert(uuidRe.test("2c8a49f2-3970-485f-83a8-336ef87b9fe0"));
  assert(!uuidRe.test("not-a-uuid"));
  assert(!uuidRe.test("2c8a49f2-3970-485f-83a8-336ef87b9fe0' OR 1=1"));
});
