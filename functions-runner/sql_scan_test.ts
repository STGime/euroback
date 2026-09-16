import { assertEquals, assertStrictEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { findPlatformSystemTableRef, scanIdentifiersAndDots, TokKind } from "./sql_scan.ts";

Deno.test("scanIdentifiersAndDots emits identifiers separated by dots", () => {
  const toks = scanIdentifiersAndDots("SELECT a.b FROM c");
  assertEquals(toks.map((t) => t.value + (t.kind === TokKind.Dot ? "<.>" : "")), [
    "SELECT",
    "a",
    "<.>",
    "b",
    "FROM",
    "c",
  ]);
});

Deno.test("scanIdentifiersAndDots skips single-quoted string bodies including doubled escapes", () => {
  const toks = scanIdentifiersAndDots(
    "SELECT 'users can''t insert' FROM posts",
  );
  const idents = toks.filter((t) => t.kind === TokKind.Ident).map((t) => t.value);
  assertEquals(idents, ["SELECT", "FROM", "posts"]);
});

Deno.test("scanIdentifiersAndDots emits double-quoted identifiers as idents (with un-escaping)", () => {
  const toks = scanIdentifiersAndDots(`SELECT "weird""name".col FROM t`);
  const idents = toks.filter((t) => t.kind === TokKind.Ident).map((t) => t.value);
  assertEquals(idents, ["SELECT", 'weird"name', "col", "FROM", "t"]);
});

Deno.test("scanIdentifiersAndDots skips line + block comments (nested)", () => {
  const toks = scanIdentifiersAndDots(
    "SELECT a -- users\n/* /* refresh_tokens */ users */ FROM t",
  );
  const idents = toks.filter((t) => t.kind === TokKind.Ident).map((t) => t.value);
  assertEquals(idents, ["SELECT", "a", "FROM", "t"]);
});

Deno.test("scanIdentifiersAndDots skips dollar-quoted bodies with tags", () => {
  const toks = scanIdentifiersAndDots(
    "CREATE FUNCTION f() RETURNS void AS $tag$ SELECT users $tag$ LANGUAGE sql",
  );
  const idents = toks.filter((t) => t.kind === TokKind.Ident).map((t) => t.value);
  assertStrictEquals(idents.includes("users"), false);
});

Deno.test("findPlatformSystemTableRef flags a bare users reference", () => {
  assertEquals(
    findPlatformSystemTableRef("SELECT email FROM users WHERE id = $1"),
    "users",
  );
});

Deno.test("findPlatformSystemTableRef flags refresh_tokens INSERT (session-forgery vector)", () => {
  assertEquals(
    findPlatformSystemTableRef(
      "INSERT INTO refresh_tokens (user_id, token_hash) VALUES ($1, $2)",
    ),
    "refresh_tokens",
  );
});

Deno.test("findPlatformSystemTableRef is case-insensitive (Postgres folds unquoted idents)", () => {
  assertEquals(
    findPlatformSystemTableRef('SELECT * FROM "USERS"'),
    "users",
  );
  assertEquals(
    findPlatformSystemTableRef("SELECT * FROM Users"),
    "users",
  );
});

Deno.test("findPlatformSystemTableRef does NOT false-positive on strings/comments/dollar-quotes", () => {
  assertStrictEquals(
    findPlatformSystemTableRef("SELECT 'users' AS msg FROM posts"),
    null,
  );
  assertStrictEquals(
    findPlatformSystemTableRef("SELECT id FROM posts -- users allowed here"),
    null,
  );
  assertStrictEquals(
    findPlatformSystemTableRef("SELECT id FROM posts /* email_tokens */"),
    null,
  );
  assertStrictEquals(
    findPlatformSystemTableRef("DO $$ SELECT users $$"),
    null,
  );
});

Deno.test("findPlatformSystemTableRef returns null for typical tenant queries", () => {
  assertStrictEquals(
    findPlatformSystemTableRef(
      "INSERT INTO posts (author_id, title, body) VALUES ($1, $2, $3) RETURNING id",
    ),
    null,
  );
  assertStrictEquals(
    findPlatformSystemTableRef(
      "SELECT id, title FROM posts WHERE approved = true ORDER BY created_at DESC",
    ),
    null,
  );
});

Deno.test("findPlatformSystemTableRef flags all documented platform tables", () => {
  for (const t of ["users", "refresh_tokens", "email_tokens", "vault_secrets", "storage_objects"]) {
    assertEquals(
      findPlatformSystemTableRef(`DELETE FROM ${t} WHERE id = $1`),
      t,
      `expected ${t} to be flagged`,
    );
  }
});
