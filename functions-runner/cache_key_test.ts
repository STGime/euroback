// Tests for the code-cache key derivation (issue #200): a version bump
// must produce a different key (immediate redeploy effect), while a
// missing/garbage version falls back to the id-only key.

import { assertEquals, assertNotEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { functionCacheKey } from "./cache_key.ts";

const ID = "0a1b2c3d-0000-4000-8000-000000000001";

Deno.test("version is part of the cache key — redeploy busts the cache (issue #200)", () => {
  assertEquals(functionCacheKey(ID, "1"), `${ID}@v1`);
  assertNotEquals(functionCacheKey(ID, "1"), functionCacheKey(ID, "2"));
});

Deno.test("missing version falls back to the id-only key (older gateway)", () => {
  assertEquals(functionCacheKey(ID, null), ID);
  assertEquals(functionCacheKey(ID, ""), ID);
});

Deno.test("non-numeric version is ignored, not interpolated", () => {
  // The header isn't HMAC-covered; a garbage value must not produce
  // unbounded distinct cache keys beyond what numbers already allow.
  assertEquals(functionCacheKey(ID, "v2; DROP"), ID);
  assertEquals(functionCacheKey(ID, "latest"), ID);
});
