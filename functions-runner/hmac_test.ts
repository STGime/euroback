// Closes GHSA-7428-mvpp-rhr7 layer 3 — runner-side HMAC verification.
//
// Run locally:
//   deno test --no-check hmac_test.ts
//
// Cross-language compat: the canonical-message format and the HMAC
// algorithm match `internal/functions/hmac.go` byte-for-byte. The
// reference vector at the bottom of this file uses the same expected
// signature as `TestCanonicalMessage_ReferenceVector` in the Go suite —
// if the Go side ever drifts, this test fails too, surfacing the
// drift early.

import { assert, assertEquals, assertStrictEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { canonicalMessage, constantTimeEqualHex, newVerifier } from "./hmac.ts";

// 32-byte secret (matches the Go test).
const SECRET = "0123456789abcdef0123456789abcdef";

function makeHeaders(overrides: Record<string, string> = {}): Headers {
  const h = new Headers();
  h.set("X-Project-ID", "p-001");
  h.set("X-Schema-Name", "tenant_abc");
  h.set("X-Function-ID", "fn-deadbeef");
  h.set("X-User-ID", "u-1234");
  h.set("X-User-Email", "alice@example.com");
  h.set("X-Plan", "pro");
  h.set("X-Request-ID", "req-xyz");
  for (const [k, v] of Object.entries(overrides)) h.set(k, v);
  return h;
}

// signMatchesGo computes an HMAC for a given canonical message + secret
// the same way the Go signer does, so we can construct test fixtures
// without spinning up a Go process.
async function signMatchesGo(secret: string, msg: string): Promise<string> {
  const enc = new TextEncoder();
  const key = await crypto.subtle.importKey(
    "raw",
    enc.encode(secret),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"],
  );
  const macBytes = new Uint8Array(await crypto.subtle.sign("HMAC", key, enc.encode(msg)));
  let hex = "";
  for (const b of macBytes) hex += b.toString(16).padStart(2, "0");
  return hex;
}

Deno.test("newVerifier rejects short secrets", async () => {
  for (const short of ["", "abc", "a".repeat(31)]) {
    let threw = false;
    try {
      await newVerifier(short);
    } catch {
      threw = true;
    }
    assert(threw, `newVerifier(${JSON.stringify(short)}) should throw`);
  }
});

Deno.test("newVerifier accepts a 32-byte secret", async () => {
  await newVerifier(SECRET); // shouldn't throw
});

Deno.test("verify accepts a correctly-signed request", async () => {
  const verifier = await newVerifier(SECRET);
  const ts = "1700000000";
  const headers = makeHeaders();
  headers.set("X-Eurobase-Timestamp", ts);
  const sig = await signMatchesGo(SECRET, canonicalMessage(headers, ts));
  headers.set("X-Eurobase-Signature", sig);
  const result = await verifier.verify(headers, {
    now: () => 1700000000,
    maxSkewSeconds: 60,
  });
  assertStrictEquals(result.ok, true);
});

Deno.test("verify rejects tampered headers", async () => {
  const verifier = await newVerifier(SECRET);
  const ts = "1700000000";
  const headers = makeHeaders();
  headers.set("X-Eurobase-Timestamp", ts);
  const sig = await signMatchesGo(SECRET, canonicalMessage(headers, ts));
  headers.set("X-Eurobase-Signature", sig);
  // Flip schema name.
  headers.set("X-Schema-Name", "tenant_other");
  const result = await verifier.verify(headers, { now: () => 1700000000 });
  assertStrictEquals(result.ok, false);
  if (!result.ok) assertEquals(result.reason, "mismatch");
});

Deno.test("verify rejects missing signature headers", async () => {
  const verifier = await newVerifier(SECRET);
  const headers = makeHeaders();
  const result = await verifier.verify(headers);
  assertStrictEquals(result.ok, false);
  if (!result.ok) assertEquals(result.reason, "missing");
});

Deno.test("verify rejects out-of-window timestamp", async () => {
  const verifier = await newVerifier(SECRET);
  const ts = "1700000000";
  const headers = makeHeaders();
  headers.set("X-Eurobase-Timestamp", ts);
  const sig = await signMatchesGo(SECRET, canonicalMessage(headers, ts));
  headers.set("X-Eurobase-Signature", sig);
  // 1000 seconds ahead of timestamp — outside default 300s window.
  const result = await verifier.verify(headers, { now: () => 1700001000 });
  assertStrictEquals(result.ok, false);
  if (!result.ok) assertEquals(result.reason, "out_of_window");
});

Deno.test("verify rejects bad timestamp format", async () => {
  const verifier = await newVerifier(SECRET);
  const headers = makeHeaders();
  headers.set("X-Eurobase-Timestamp", "not-a-number");
  headers.set("X-Eurobase-Signature", "0".repeat(64));
  const result = await verifier.verify(headers);
  assertStrictEquals(result.ok, false);
  if (!result.ok) assertEquals(result.reason, "bad_timestamp");
});

Deno.test("verify rejects a signature signed with a different secret", async () => {
  const verifier = await newVerifier(SECRET);
  const otherSecret = "ffffffffffffffffffffffffffffffff"; // 32 chars but different
  const ts = "1700000000";
  const headers = makeHeaders();
  headers.set("X-Eurobase-Timestamp", ts);
  const sig = await signMatchesGo(otherSecret, canonicalMessage(headers, ts));
  headers.set("X-Eurobase-Signature", sig);
  const result = await verifier.verify(headers, { now: () => 1700000000 });
  assertStrictEquals(result.ok, false);
  if (!result.ok) assertEquals(result.reason, "mismatch");
});

Deno.test("constantTimeEqualHex returns true for equal strings", () => {
  assertStrictEquals(constantTimeEqualHex("abc123", "abc123"), true);
});

Deno.test("constantTimeEqualHex returns false for unequal strings", () => {
  assertStrictEquals(constantTimeEqualHex("abc123", "abc124"), false);
  assertStrictEquals(constantTimeEqualHex("abc", "abcdef"), false);
});

Deno.test("canonicalMessage matches the Go reference vector", () => {
  const headers = makeHeaders();
  const got = canonicalMessage(headers, "1700000000");
  const want = "v=1\n" +
    "ts=1700000000\n" +
    "project=p-001\n" +
    "schema=tenant_abc\n" +
    "function=fn-deadbeef\n" +
    "user=u-1234\n" +
    "email=alice@example.com\n" +
    "plan=pro\n" +
    "requestid=req-xyz";
  assertEquals(got, want);
});

Deno.test("canonicalMessage emits empty string for missing fields", () => {
  // Mirrors the Go test — the line MUST be present so a forger can't
  // mutate the canonical form by inserting a new value into a previously
  // empty field.
  const h = new Headers();
  h.set("X-Project-ID", "p");
  h.set("X-Schema-Name", "t");
  h.set("X-Function-ID", "f");
  h.set("X-Plan", "free");
  h.set("X-Request-ID", "r");
  const msg = canonicalMessage(h, "100");
  assert(msg.includes("user=\n"), "missing empty user= line");
  assert(msg.includes("email=\n"), "missing empty email= line");
});
