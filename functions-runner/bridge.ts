// Message protocol shared between the runner (parent) and the
// per-invocation Web Worker (child). Closes GHSA-7428-mvpp-rhr7 layer 2.
//
// The worker runs with `permissions: 'none'` — no net, no env, no
// read/write/run/ffi. User JS therefore cannot reach Postgres, the
// filesystem, or environment variables directly. All capabilities a
// function needs (DB queries, vault reads, log emission) are proxied to
// the parent over postMessage and run there with the per-tenant role
// from PR 3a.
//
// All payloads are structured-clone-safe (Uint8Array bodies, Headers as
// entry tuples, primitive types elsewhere) so no manual JSON encoding is
// needed — the browser/Deno runtime serialises automatically.

export interface SerializedRequest {
  method: string;
  url: string;
  // Headers as a list of [name, value] tuples. Order preserved (Headers
  // class collapses duplicates per spec, so this is canonical).
  headers: [string, string][];
  // Body as a Uint8Array (raw bytes) or null if no body. Already
  // size-bounded by the request-size limit at the runner's HTTP layer.
  body: Uint8Array | null;
}

export interface SerializedResponse {
  status: number;
  headers: [string, string][];
  body: Uint8Array;
}

// Parent → Worker messages.
export type ParentToWorker =
  // First message: load the user's code into the worker via Function
  // constructor. Module-import with `export default` isn't supported
  // because Deno's permission model blocks dynamic imports of unknown
  // origins under `permissions: 'none'`. The runner accepts user code
  // that exposes the handler one of two ways (matches the legacy
  // AsyncFunction-based runner so existing functions keep working):
  //   globalThis.handler = (req, ctx) => ...
  //   module.exports = { default: (req, ctx) => ... }
  //   exports.default = (req, ctx) => ...
  | { type: "load"; code: string }
  // Second message: drive an invocation.
  | {
      type: "invoke";
      request: SerializedRequest;
      env: Record<string, string>;
      user: { id: string; email: string } | null;
      requestId: string;
      timeoutMs: number;
    }
  // RPC result for `db.sql`. Mirrors the call's `id` so async handlers
  // route to the right Promise.
  | { type: "db.sql.result"; id: string; rows?: unknown; error?: string }
  // RPC result for `vault.get`.
  | { type: "vault.get.result"; id: string; value: string | null };

// Worker → Parent messages.
export type WorkerToParent =
  | { type: "loaded" }
  | { type: "result"; response: SerializedResponse }
  | { type: "error"; message: string }
  // RPC call from worker to parent for SQL execution. Parent runs the
  // query under the per-tenant role and posts back db.sql.result.
  | { type: "db.sql.call"; id: string; query: string; params: unknown[] }
  | { type: "vault.get.call"; id: string; name: string }
  | { type: "log"; level: "INFO" | "WARN" | "ERROR"; msg: string; data?: unknown };
