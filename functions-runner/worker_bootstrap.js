// Worker bootstrap module — runs INSIDE a permission-none Web Worker.
//
// Closes GHSA-7428-mvpp-rhr7 layer 2.
//
// This script is loaded by the runner's main thread into a
// per-invocation Worker that has `permissions: 'none'`:
//   - net: false → user JS cannot fetch, open sockets, or connect to Postgres
//   - env: false → user JS cannot read DATABASE_URL_FUNCTION_RUNNER
//   - read: false → user JS cannot read /proc, /etc, or any file
//   - write/run/ffi: false → no escape via FS, subprocess, or native code
//
// User JS still has the full V8/web platform — Promise, fetch (without
// net throws), JSON, crypto.subtle, etc. Capability tokens (DB, vault,
// log) come over postMessage from the parent.
//
// Plain JS rather than TS so the worker doesn't need a TypeScript loader
// (no `read` permission means it can't load tsconfig anyway).

(() => {
  // Pending RPC calls to the parent, keyed by call id.
  const pending = new Map();

  function rpc(type, payload) {
    const id = crypto.randomUUID();
    return new Promise((resolve, reject) => {
      pending.set(id, { resolve, reject });
      self.postMessage({ type, id, ...payload });
    });
  }

  // Catch RPC results and route to the right pending Promise.
  // Each call's wrapper in ctx.* knows what shape it expects, so this
  // resolves with the whole message minus type/id and lets the caller
  // pick out the field it cares about. (Pre-#85 db.sql/vault.get
  // helpers above this layer continue to consume `.rows` / `.value`.)
  function handleRpcResult(msg) {
    const handler = pending.get(msg.id);
    if (!handler) return;
    pending.delete(msg.id);
    if (msg.error) {
      handler.reject(new Error(String(msg.error)));
      return;
    }
    if (Object.prototype.hasOwnProperty.call(msg, "rows")) {
      handler.resolve(msg.rows);
      return;
    }
    if (Object.prototype.hasOwnProperty.call(msg, "value")) {
      handler.resolve(msg.value);
      return;
    }
    // Storage RPCs: hand back the full payload object. Strip type/id so
    // the caller doesn't see the bridge plumbing.
    const { type: _t, id: _i, ...rest } = msg;
    handler.resolve(rest);
  }

  function buildRequest(serialized) {
    const headers = new Headers();
    for (const [k, v] of serialized.headers) headers.set(k, v);
    const init = { method: serialized.method, headers };
    if (serialized.body) {
      // Uint8Array survives structured clone. Pass it directly as the
      // body; Request accepts Uint8Array.
      init.body = serialized.body;
    }
    return new Request(serialized.url, init);
  }

  async function serializeResponse(response) {
    if (!(response instanceof Response)) {
      // Allow the user to return any JSON-serialisable value; wrap in a
      // Response so downstream serialization is uniform.
      response = new Response(JSON.stringify(response), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }
    const buf = await response.arrayBuffer();
    return {
      status: response.status,
      headers: [...response.headers.entries()],
      body: new Uint8Array(buf),
    };
  }

  // Holder for the user's handler. Populated by the `load` message.
  let userHandler = null;

  // Detection suffix appended after user code. Picks up:
  //   globalThis.handler = (req, ctx) => ... (most common)
  //   module.exports = (req, ctx) => ... (CommonJS)
  //   exports.default = (req, ctx) => ... (CommonJS default)
  //
  // Cannot detect `export default` syntax — that requires loading the
  // user code as a module, which `permissions: 'none'` disallows. The
  // runner documents this; existing functions already use the
  // globalThis-or-exports patterns.
  const HANDLER_DETECT = `;
    if (typeof handler === 'function') globalThis.__userHandler = handler;
    else if (typeof exports !== 'undefined' && typeof exports.default === 'function') globalThis.__userHandler = exports.default;
    else if (typeof exports !== 'undefined' && typeof exports === 'function') globalThis.__userHandler = exports;
    else if (typeof module !== 'undefined' && typeof module.exports === 'function') globalThis.__userHandler = module.exports;
  `;

  function loadUser(code) {
    // Both `module` and `exports` are pre-defined so CommonJS-shaped
    // user code doesn't ReferenceError. They're throwaway; the
    // detection suffix copies any handler into __userHandler.
    const wrapped = `
      "use strict";
      const module = { exports: {} };
      const exports = module.exports;
      ${code}
      ${HANDLER_DETECT}
    `;
    // Function constructor evaluates in the worker's global context.
    // The worker has `permissions: 'none'`, so even though Function is
    // available, any capability-requiring API throws or returns
    // undefined.
    new Function(wrapped)();
    userHandler = globalThis.__userHandler;
    if (typeof userHandler !== "function") {
      throw new Error(
        "Function must export a default handler — assign to globalThis.handler, exports.default, or module.exports",
      );
    }
  }

  function makeCtx(invokeMsg) {
    const log = {
      info: (m, d) => self.postMessage({ type: "log", level: "INFO", msg: String(m), data: d }),
      warn: (m, d) => self.postMessage({ type: "log", level: "WARN", msg: String(m), data: d }),
      error: (m, d) => self.postMessage({ type: "log", level: "ERROR", msg: String(m), data: d }),
    };
    return {
      db: {
        sql: (query, params = []) => rpc("db.sql.call", { query, params }),
      },
      vault: {
        get: (name) => rpc("vault.get.call", { name }),
      },
      // ctx.storage — closes #85. The parent process calls back to the
      // gateway's HMAC-protected /internal/functions/storage endpoints.
      // Bodies are passed as Uint8Array (structured-cloneable). Each
      // helper returns a shaped object the function can destructure.
      storage: {
        async upload(key, body, opts = {}) {
          if (!(body instanceof Uint8Array)) {
            // Accept ArrayBuffer / Blob / string for ergonomics; convert
            // to Uint8Array before sending across the postMessage bridge.
            if (body instanceof ArrayBuffer) body = new Uint8Array(body);
            else if (typeof body === "string") body = new TextEncoder().encode(body);
            else if (body && typeof body.arrayBuffer === "function") body = new Uint8Array(await body.arrayBuffer());
            else throw new Error("ctx.storage.upload: body must be Uint8Array, ArrayBuffer, Blob, or string");
          }
          return rpc("storage.upload.call", { key, body, contentType: opts.contentType });
        },
        createSignedUrl(key, operation, opts = {}) {
          if (operation !== "upload" && operation !== "download") {
            return Promise.reject(new Error("ctx.storage.createSignedUrl: operation must be 'upload' or 'download'"));
          }
          return rpc("storage.signed_url.call", {
            key,
            operation,
            expiresIn: opts.expiresIn,
            contentType: opts.contentType,
          });
        },
        delete(key) {
          return rpc("storage.delete.call", { key });
        },
      },
      env: invokeMsg.env || {},
      user: invokeMsg.user || null,
      requestId: invokeMsg.requestId,
      log,
    };
  }

  async function handleInvoke(msg) {
    if (!userHandler) {
      throw new Error("worker invoked before user code was loaded");
    }
    const req = buildRequest(msg.request);
    const ctx = makeCtx(msg);

    // Soft timeout: post an `error` message after timeoutMs and let the
    // parent terminate the worker. We can't AbortController-wrap the
    // user handler reliably, so the parent's `setTimeout(terminate)`
    // is the hard backstop.
    let response;
    try {
      response = await userHandler(req, ctx);
    } catch (err) {
      const msgText = err && err.message ? err.message : String(err);
      self.postMessage({ type: "error", message: msgText });
      return;
    }
    const serialized = await serializeResponse(response);
    self.postMessage({ type: "result", response: serialized });
  }

  self.addEventListener("message", (e) => {
    const msg = e.data;
    if (!msg || typeof msg.type !== "string") return;

    if (
      msg.type === "db.sql.result" ||
      msg.type === "vault.get.result" ||
      msg.type === "storage.upload.result" ||
      msg.type === "storage.signed_url.result" ||
      msg.type === "storage.delete.result"
    ) {
      handleRpcResult(msg);
      return;
    }
    if (msg.type === "load") {
      try {
        loadUser(msg.code);
        self.postMessage({ type: "loaded" });
      } catch (err) {
        const text = err && err.message ? err.message : String(err);
        self.postMessage({ type: "error", message: text });
      }
      return;
    }
    if (msg.type === "invoke") {
      handleInvoke(msg).catch((err) => {
        const text = err && err.message ? err.message : String(err);
        self.postMessage({ type: "error", message: text });
      });
      return;
    }
  });
})();
