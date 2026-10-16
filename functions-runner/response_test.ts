import { assert, assertEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { buildResponseBody } from "./response.ts";

// Regression: a function returning 204 (or 205 / 304) made the runner's
// `new Response(body, { status })` throw, leaving the request unanswered.
Deno.test("null-body statuses get no body and build a Response", () => {
  for (const status of [204, 205, 304]) {
    const built = buildResponseBody(status, new Uint8Array());
    assert(built.ok, String(status));
    assertEquals(built.body, null);
    const res = new Response(built.body, { status: built.status }); // must not throw
    assertEquals(res.status, status);
    // Even when the function sent bytes with a 204, they're dropped.
    const withBytes = buildResponseBody(status, new TextEncoder().encode("ignored"));
    assert(withBytes.ok && withBytes.body === null);
  }
});

Deno.test("ordinary statuses keep their body", async () => {
  const built = buildResponseBody(201, new TextEncoder().encode("made"));
  assert(built.ok);
  assertEquals(await new Response(built.body, { status: built.status }).text(), "made");
  const empty = buildResponseBody(200, null);
  assert(empty.ok);
  assertEquals(await new Response(empty.body, { status: 200 }).text(), "");
});

Deno.test("invalid statuses are the function's error, not a throw", () => {
  for (const status of [0, 100, 101, 199, 600, 1000, 200.5, NaN, "200", undefined]) {
    const built = buildResponseBody(status, null);
    assert(!built.ok, String(status));
    assert(/invalid HTTP status/.test(built.error));
  }
});

// End to end through the real worker bootstrap: a handler returning
// `new Response(null, { status: 204 })` serializes and is answered.
Deno.test("a worker returning 204 is answered", async () => {
  const bootstrap = new URL("./worker_bootstrap.js", import.meta.url);
  const worker = new Worker(bootstrap, { type: "module" });
  const result = await new Promise<{ status: number; body: Uint8Array }>((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error("no result")), 5000);
    worker.addEventListener("message", (e: MessageEvent) => {
      const msg = e.data;
      if (msg.type === "loaded") {
        worker.postMessage({
          type: "invoke",
          request: { method: "POST", url: "http://fn.local/", headers: [], body: null },
          env: {},
          user: null,
          requestId: "t",
          timeoutMs: 5000,
        });
      } else if (msg.type === "result") {
        clearTimeout(timer);
        resolve(msg.response);
      } else if (msg.type === "error") {
        clearTimeout(timer);
        reject(new Error(msg.message));
      }
    });
    worker.postMessage({ type: "load", code: "globalThis.handler = async () => new Response(null, { status: 204 });" });
  });
  worker.terminate();
  const built = buildResponseBody(result.status, result.body);
  assert(built.ok);
  const res = new Response(built.body, { status: built.status });
  assertEquals(res.status, 204);
  assertEquals(res.body, null);
});
