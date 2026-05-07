// Tests for the parent ↔ worker message protocol.
//
// The protocol carries user-controlled bytes (Request body, Response
// body, log payloads) across the worker boundary, so structured-clone
// safety + payload-shape stability are load-bearing for the security
// contract from PR 3b. These tests assert the shape doesn't drift.
//
// Run locally with: deno test --no-check bridge_test.ts

import { assertEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import type {
  ParentToWorker,
  SerializedRequest,
  SerializedResponse,
  WorkerToParent,
} from "./bridge.ts";

Deno.test("SerializedRequest survives structured clone", () => {
  const original: SerializedRequest = {
    method: "POST",
    url: "https://example.com/v1/functions/foo",
    headers: [
      ["content-type", "application/json"],
      ["x-request-id", "abc-123"],
    ],
    body: new Uint8Array([1, 2, 3, 4, 5]),
  };
  const clone = structuredClone(original);
  assertEquals(clone.method, original.method);
  assertEquals(clone.url, original.url);
  assertEquals(clone.headers, original.headers);
  assertEquals(clone.body?.length, 5);
  assertEquals(clone.body?.[2], 3);
});

Deno.test("SerializedRequest with null body clones cleanly", () => {
  const original: SerializedRequest = {
    method: "GET",
    url: "https://example.com/",
    headers: [],
    body: null,
  };
  const clone = structuredClone(original);
  assertEquals(clone.body, null);
});

Deno.test("SerializedResponse survives structured clone with binary body", () => {
  const body = new Uint8Array(1024);
  for (let i = 0; i < body.length; i++) body[i] = i & 0xff;
  const original: SerializedResponse = {
    status: 200,
    headers: [["content-type", "application/octet-stream"]],
    body,
  };
  const clone = structuredClone(original);
  assertEquals(clone.status, 200);
  assertEquals(clone.body.length, 1024);
  assertEquals(clone.body[0], 0);
  assertEquals(clone.body[256], 0);
  assertEquals(clone.body[1023], 255);
});

Deno.test("ParentToWorker.load message matches the discriminator type", () => {
  const m: ParentToWorker = { type: "load", code: "globalThis.handler = () => new Response('ok')" };
  assertEquals(m.type, "load");
  assertEquals(typeof m.code, "string");
});

Deno.test("ParentToWorker.invoke includes all required fields", () => {
  const m: ParentToWorker = {
    type: "invoke",
    request: { method: "GET", url: "/", headers: [], body: null },
    env: { FOO: "bar" },
    user: { id: "u1", email: "u1@example.com" },
    requestId: "req-1",
    timeoutMs: 10_000,
  };
  // structuredClone ensures the whole shape is clone-safe — no
  // functions, no Symbols, etc.
  const clone = structuredClone(m);
  assertEquals(clone.type, "invoke");
  assertEquals(clone.user?.email, "u1@example.com");
  assertEquals(clone.timeoutMs, 10_000);
});

Deno.test("WorkerToParent.error has a string message", () => {
  const m: WorkerToParent = { type: "error", message: "something went wrong" };
  assertEquals(m.type, "error");
  assertEquals(m.message, "something went wrong");
});

Deno.test("WorkerToParent.db.sql.call carries id, query, params", () => {
  const m: WorkerToParent = {
    type: "db.sql.call",
    id: "rpc-1",
    query: "SELECT * FROM events WHERE user_id = $1",
    params: ["00000000-0000-0000-0000-000000000001"],
  };
  const clone = structuredClone(m);
  assertEquals(clone.id, "rpc-1");
  assertEquals(clone.params.length, 1);
});

Deno.test("Headers tuples preserve case the way Headers normalises", () => {
  // Headers normalise names to lowercase. The serialized form should
  // capture that — round-tripping through the boundary should yield a
  // Headers object that matches what the original did.
  const original = new Headers();
  original.set("Content-Type", "application/json");
  original.append("X-Custom", "v1");
  const serialized = [...original.entries()];
  const rebuilt = new Headers();
  for (const [k, v] of serialized) rebuilt.set(k, v);
  assertEquals(rebuilt.get("content-type"), "application/json");
  assertEquals(rebuilt.get("x-custom"), "v1");
});
