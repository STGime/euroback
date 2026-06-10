// End-to-end Deno tests for the worker-isolate sandbox introduced in
// PR 3b (advisory GHSA-7428-mvpp-rhr7 layer 2). Updated for #83 so the
// worker now has `net: true` to let edge functions call external APIs;
// internal egress is blocked by k8s NetworkPolicy in production rather
// than at the Deno permissions layer.
//
// These tests spawn a real Web Worker with the same permissions
// configuration the runner uses in production, load user code into it,
// invoke it, and assert that the credential-isolation boundaries hold:
//
//   - Reading Deno.env returns nothing (load-bearing — protects
//     VAULT_ENCRYPTION_KEY, DATABASE_URL, etc.).
//   - Reading the filesystem fails.
//   - postMessage RPC for db.sql / vault.get is the only DB / vault path.
//
// Outbound network is intentionally allowed; the corresponding test was
// dropped in #83.
//
// CI runs these via the test-functions-runner job (.github/workflows/ci.yml).

import { assert, assertEquals, assertStrictEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import type { ParentToWorker, WorkerToParent } from "./bridge.ts";

const BOOTSTRAP_PATH = new URL("./worker_bootstrap.js", import.meta.url);

interface InvokeResult {
  ok: boolean;
  status?: number;
  body?: string;
  error?: string;
  rpcCalls: { type: string; payload: unknown }[];
}

// Spawn a worker, load user code, invoke once, return the response.
// Mocks db.sql / vault.get RPC handlers so the test can assert what
// the user code asked for (and reply with controlled values).
function spawnAndInvoke(opts: {
  userCode: string;
  request?: { method?: string; url?: string; headers?: [string, string][]; body?: Uint8Array | null };
  env?: Record<string, string>;
  user?: { id: string; email: string } | null;
  // deno-lint-ignore no-explicit-any
  dbSqlHandler?: (query: string, params: unknown[]) => unknown | Promise<any>;
  vaultGetHandler?: (name: string) => string | null;
  timeoutMs?: number;
}): Promise<InvokeResult> {
  return new Promise((resolve) => {
    const worker = new Worker(BOOTSTRAP_PATH, {
      type: "module",
      deno: {
        // Mirror the production permission set in server.ts. Every key
        // is listed explicitly because unspecified permissions inherit
        // from the parent process.
        permissions: {
          net: true,
          env: false,
          read: false,
          write: false,
          run: false,
          ffi: false,
          sys: false,
          import: false,
        },
      },
    });
    const rpcCalls: { type: string; payload: unknown }[] = [];
    let settled = false;

    const cleanup = () => {
      if (settled) return;
      settled = true;
      try {
        worker.terminate();
      } catch (_) { /* */ }
    };

    const timer = setTimeout(() => {
      cleanup();
      resolve({ ok: false, error: "test timeout", rpcCalls });
    }, opts.timeoutMs ?? 5_000);

    worker.addEventListener("error", (e) => {
      clearTimeout(timer);
      cleanup();
      resolve({ ok: false, error: e.message, rpcCalls });
    });

    worker.addEventListener("message", async (event) => {
      const msg = event.data as WorkerToParent;
      if (!msg) return;
      switch (msg.type) {
        case "loaded": {
          const invoke: ParentToWorker = {
            type: "invoke",
            request: {
              method: opts.request?.method ?? "GET",
              url: opts.request?.url ?? "https://test/invoke",
              headers: opts.request?.headers ?? [],
              body: opts.request?.body ?? null,
            },
            env: opts.env ?? {},
            user: opts.user ?? null,
            requestId: "test-req-1",
            timeoutMs: opts.timeoutMs ?? 5_000,
          };
          worker.postMessage(invoke);
          break;
        }
        case "result": {
          clearTimeout(timer);
          const body = new TextDecoder().decode(msg.response.body);
          cleanup();
          resolve({
            ok: true,
            status: msg.response.status,
            body,
            rpcCalls,
          });
          break;
        }
        case "error": {
          clearTimeout(timer);
          cleanup();
          resolve({ ok: false, error: msg.message, rpcCalls });
          break;
        }
        case "db.sql.call": {
          rpcCalls.push({ type: "db.sql.call", payload: { query: msg.query, params: msg.params } });
          try {
            const rows = opts.dbSqlHandler
              ? await opts.dbSqlHandler(msg.query, msg.params)
              : [];
            const reply: ParentToWorker = { type: "db.sql.result", id: msg.id, rows };
            worker.postMessage(reply);
          } catch (err) {
            const reply: ParentToWorker = {
              type: "db.sql.result",
              id: msg.id,
              error: err instanceof Error ? err.message : String(err),
            };
            worker.postMessage(reply);
          }
          break;
        }
        case "vault.get.call": {
          rpcCalls.push({ type: "vault.get.call", payload: { name: msg.name } });
          const value = opts.vaultGetHandler ? opts.vaultGetHandler(msg.name) : null;
          const reply: ParentToWorker = { type: "vault.get.result", id: msg.id, value };
          worker.postMessage(reply);
          break;
        }
        case "log":
          rpcCalls.push({ type: "log", payload: { level: msg.level, msg: msg.msg, data: msg.data } });
          break;
      }
    });

    queueMicrotask(() => {
      const load: ParentToWorker = { type: "load", code: opts.userCode };
      worker.postMessage(load);
    });
  });
}

// ──────────────────────────────────────────────────────────────────
// Capability isolation tests — these are the load-bearing assertions
// for the sandbox security claim.
// ──────────────────────────────────────────────────────────────────

Deno.test("user JS cannot read Deno.env (load-bearing for credential isolation)", async () => {
  const result = await spawnAndInvoke({
    userCode: `
      globalThis.handler = async () => {
        let leaked = null;
        let errMsg = null;
        try {
          // This is what an attacker would try post-PR-3a to grab
          // DATABASE_URL_FUNCTION_RUNNER and connect directly.
          leaked = Deno.env.get("DATABASE_URL_FUNCTION_RUNNER");
        } catch (e) {
          errMsg = e && e.message ? e.message : String(e);
        }
        return new Response(JSON.stringify({ leaked, errMsg }), {
          headers: { "Content-Type": "application/json" },
        });
      };
    `,
  });
  assert(result.ok, "invocation failed: " + result.error);
  const parsed = JSON.parse(result.body!);
  // Either Deno.env access throws (PermissionDenied) or returns
  // undefined. Both close the leak path.
  assert(
    parsed.leaked === undefined || parsed.leaked === null,
    "Deno.env.get returned a value — credential leak: " + parsed.leaked,
  );
});

Deno.test("user JS cannot Deno.readTextFile arbitrary paths", async () => {
  const result = await spawnAndInvoke({
    userCode: `
      globalThis.handler = async () => {
        let bytes = null;
        let errMsg = null;
        try {
          bytes = await Deno.readTextFile("/etc/passwd");
        } catch (e) {
          errMsg = e && e.message ? e.message : String(e);
        }
        return new Response(JSON.stringify({ leaked: bytes !== null, errMsg }), {
          headers: { "Content-Type": "application/json" },
        });
      };
    `,
  });
  assert(result.ok, "invocation failed: " + result.error);
  const parsed = JSON.parse(result.body!);
  assertStrictEquals(parsed.leaked, false, "filesystem read succeeded — sandbox is broken");
});

// (The previous "user JS cannot fetch arbitrary URLs" test was removed
// in #83 — outbound network is now intentionally allowed at the Deno
// permission layer. Cluster-internal egress is blocked by NetworkPolicy
// at runtime, which CI cannot exercise; that's verified manually after
// each deploy.)

// ──────────────────────────────────────────────────────────────────
// Functional tests — user JS still does what it should via the RPC bridge.
// ──────────────────────────────────────────────────────────────────

Deno.test("user JS gets handler shape via globalThis.handler", async () => {
  const result = await spawnAndInvoke({
    userCode: `
      globalThis.handler = async (req, ctx) => {
        return new Response("hello " + ctx.requestId, { status: 200 });
      };
    `,
  });
  assert(result.ok);
  assertEquals(result.status, 200);
  assertEquals(result.body, "hello test-req-1");
});

Deno.test("user JS gets handler shape via module.exports", async () => {
  const result = await spawnAndInvoke({
    userCode: `
      module.exports = async (req, ctx) => {
        return new Response("via exports", { status: 201 });
      };
    `,
  });
  assert(result.ok);
  assertEquals(result.status, 201);
  assertEquals(result.body, "via exports");
});

Deno.test("user JS gets handler shape via exports.default", async () => {
  const result = await spawnAndInvoke({
    userCode: `
      exports.default = async (req, ctx) => {
        return new Response("via exports.default", { status: 202 });
      };
    `,
  });
  assert(result.ok);
  assertEquals(result.status, 202);
});

Deno.test("esbuild CommonJS output (export default) is detected (issue #189)", async () => {
  // Literal shape of `esbuild --format=cjs` output for
  // `export default async (req) => ...` — module.exports is REPLACED
  // with a fresh object whose .default is a getter, so the plain
  // exports.default check never sees it.
  const result = await spawnAndInvoke({
    userCode: `
      var __defProp = Object.defineProperty;
      var __export = (target, all) => {
        for (var name in all)
          __defProp(target, name, { get: all[name], enumerable: true });
      };
      var stdin_exports = {};
      __export(stdin_exports, { default: () => stdin_default });
      module.exports = stdin_exports;
      var stdin_default = async (req, ctx) => {
        return new Response("via esbuild cjs", { status: 203 });
      };
    `,
  });
  assert(result.ok, `expected detection of module.exports.default: ${result.error ?? ""}`);
  assertEquals(result.status, 203);
  assertEquals(result.body, "via esbuild cjs");
});

Deno.test("require() of a third-party module fails at load with a clear message (issue #189)", async () => {
  // Third-party imports compile to require() calls; the bootstrap stub
  // must reject them with an actionable error, not a ReferenceError.
  const result = await spawnAndInvoke({
    userCode: `
      const { serve } = require("https://deno.land/std/http/server.ts");
      globalThis.handler = async () => new Response("unreachable");
    `,
  });
  assert(!result.ok);
  assert(
    String(result.error).includes("third-party imports are not supported"),
    `expected the require-stub message, got: ${result.error}`,
  );
});

Deno.test("ctx.db.sql proxies to the parent via RPC", async () => {
  let recordedQuery = "";
  let recordedParams: unknown[] = [];
  const result = await spawnAndInvoke({
    userCode: `
      globalThis.handler = async (req, ctx) => {
        const rows = await ctx.db.sql("SELECT * FROM users WHERE id = $1", ["u1"]);
        return Response.json(rows);
      };
    `,
    dbSqlHandler: (query, params) => {
      recordedQuery = query;
      recordedParams = params;
      return [{ id: "u1", email: "u1@example.com" }];
    },
  });
  assert(result.ok, result.error);
  assertEquals(recordedQuery, "SELECT * FROM users WHERE id = $1");
  assertEquals(recordedParams, ["u1"]);
  const parsed = JSON.parse(result.body!);
  assertEquals(parsed.length, 1);
  assertEquals(parsed[0].id, "u1");
});

Deno.test("ctx.db.sql RPC error surfaces as a JS Error in user code", async () => {
  const result = await spawnAndInvoke({
    userCode: `
      globalThis.handler = async (req, ctx) => {
        try {
          await ctx.db.sql("SELECT * FROM tenant_other.users");
          return Response.json({ ok: true });
        } catch (e) {
          return Response.json({ ok: false, msg: e.message });
        }
      };
    `,
    dbSqlHandler: () => { throw new Error("permission denied for schema tenant_other"); },
  });
  assert(result.ok);
  const parsed = JSON.parse(result.body!);
  assertEquals(parsed.ok, false);
  assert(parsed.msg.includes("permission denied"), "expected user-side error to include parent's message");
});

Deno.test("ctx.env carries the function's env_vars (no host env leak)", async () => {
  const result = await spawnAndInvoke({
    userCode: `
      globalThis.handler = async (req, ctx) => Response.json({
        function_var: ctx.env.MY_VAR,
        keys: Object.keys(ctx.env),
      });
    `,
    env: { MY_VAR: "hello" },
  });
  assert(result.ok);
  const parsed = JSON.parse(result.body!);
  assertEquals(parsed.function_var, "hello");
  // Only the function's declared env_vars; no DATABASE_URL,
  // VAULT_ENCRYPTION_KEY, etc.
  assertEquals(parsed.keys, ["MY_VAR"]);
});

Deno.test("ctx.user is null when no end-user JWT is present", async () => {
  const result = await spawnAndInvoke({
    userCode: `
      globalThis.handler = async (req, ctx) => Response.json({ user: ctx.user });
    `,
    user: null,
  });
  assert(result.ok);
  const parsed = JSON.parse(result.body!);
  assertEquals(parsed.user, null);
});

Deno.test("user code that throws synchronously surfaces a 500 error", async () => {
  const result = await spawnAndInvoke({
    userCode: `
      globalThis.handler = async (req, ctx) => {
        throw new Error("user error: forbidden");
      };
    `,
  });
  assertStrictEquals(result.ok, false);
  assert(result.error?.includes("user error: forbidden"), "expected user error message to surface; got: " + result.error);
});

Deno.test("missing handler is reported as a clear error", async () => {
  const result = await spawnAndInvoke({
    userCode: `// no handler defined`,
  });
  assertStrictEquals(result.ok, false);
  assert(
    result.error?.toLowerCase().includes("handler"),
    "expected handler-missing error; got: " + result.error,
  );
});

Deno.test("ctx.log is forwarded to the parent as log messages", async () => {
  const result = await spawnAndInvoke({
    userCode: `
      globalThis.handler = async (req, ctx) => {
        ctx.log.info("first message", { count: 1 });
        ctx.log.warn("second message");
        ctx.log.error("third message", { code: "X" });
        return new Response("ok");
      };
    `,
  });
  assert(result.ok);
  const logs = result.rpcCalls.filter((c) => c.type === "log");
  assertEquals(logs.length, 3);
  // deno-lint-ignore no-explicit-any
  assertEquals((logs[0].payload as any).level, "INFO");
  // deno-lint-ignore no-explicit-any
  assertEquals((logs[1].payload as any).level, "WARN");
  // deno-lint-ignore no-explicit-any
  assertEquals((logs[2].payload as any).level, "ERROR");
});

Deno.test("user JS that returns a non-Response value gets wrapped as JSON", async () => {
  const result = await spawnAndInvoke({
    userCode: `
      globalThis.handler = async (req, ctx) => ({ hello: "world" });
    `,
  });
  assert(result.ok);
  assertEquals(result.status, 200);
  assertEquals(JSON.parse(result.body!), { hello: "world" });
});

Deno.test("Request body is delivered to user code as the original bytes", async () => {
  const body = new TextEncoder().encode(`{"input": 42}`);
  const result = await spawnAndInvoke({
    request: { method: "POST", body, headers: [["content-type", "application/json"]] },
    userCode: `
      globalThis.handler = async (req, ctx) => {
        const text = await req.text();
        return new Response("echo: " + text);
      };
    `,
  });
  assert(result.ok);
  assertEquals(result.body, 'echo: {"input": 42}');
});
