/**
 * Eurobase Edge Functions Runner
 *
 * Deno HTTP server that receives proxied requests from the Gateway,
 * loads function code from PostgreSQL, and executes it in a sandboxed context.
 *
 * This server runs inside the Kapsule cluster on an internal ClusterIP — it
 * is NEVER exposed to the public internet. Only the Gateway can reach it.
 */

import { quoteIdent, rlsContextStatements, tenantFuncRole, validIdentRe } from "./role.ts";
import type {
  ParentToWorker,
  SerializedRequest,
  SerializedResponse,
  WorkerToParent,
} from "./bridge.ts";
import { newVerifier, type Verifier } from "./hmac.ts";
import { resolveVaultSecret } from "./vault.ts";
import { createSignedUrl, deleteObject, uploadObject } from "./storage.ts";

// Closes GHSA-7428-mvpp-rhr7 layer 1: the runner now connects as
// `eurobase_function_runner`, a role with no direct grants on any tenant
// schema. Each invocation does `SET LOCAL ROLE <schema>_func` inside a
// transaction so user SQL physically cannot reach other tenants — the
// `<schema>_func` role is granted only on its own schema.
//
// The legacy DATABASE_URL env var is kept as a fallback during the
// rollout window so the runner doesn't crash on a partial-state deploy
// (k8s Secret update racing with pod restart). Once
// DATABASE_URL_FUNCTION_RUNNER is in every environment, the fallback
// can be removed.
const DB_URL = Deno.env.get("DATABASE_URL_FUNCTION_RUNNER") ?? Deno.env.get("DATABASE_URL") ?? "";
const PORT = parseInt(Deno.env.get("PORT") ?? "8000");
const MAX_CONCURRENT = parseInt(Deno.env.get("MAX_CONCURRENT_ISOLATES") ?? "50");
const CODE_CACHE_SIZE = parseInt(Deno.env.get("CODE_CACHE_SIZE") ?? "200");
const CODE_CACHE_TTL = parseInt(Deno.env.get("CODE_CACHE_TTL_SECONDS") ?? "300") * 1000;

// HMAC settings — closes GHSA-7428-mvpp-rhr7 layer 3.
//
// FUNCTIONS_RUNNER_HMAC_SECRET: shared secret with the gateway, ≥32 bytes.
// FUNCTIONS_RUNNER_HMAC_REQUIRE_SIGNED:
//   - "true"  → strict mode: missing/invalid signatures return 401.
//   - other   → soft mode: warn-only on missing signatures, but invalid
//               signatures are still rejected. Used briefly during the
//               rollout window where some gateway pods may not yet have
//               the secret.
const HMAC_SECRET = Deno.env.get("FUNCTIONS_RUNNER_HMAC_SECRET") ?? "";
const HMAC_REQUIRE_SIGNED = (Deno.env.get("FUNCTIONS_RUNNER_HMAC_REQUIRE_SIGNED") ?? "").toLowerCase() === "true";

// ── Size limits ──

const REQUEST_SIZE_FREE = 1 * 1024 * 1024;   // 1 MB
const REQUEST_SIZE_PRO = 5 * 1024 * 1024;     // 5 MB
const RESPONSE_SIZE_LIMIT = 5 * 1024 * 1024;  // 5 MB
const LOG_OUTPUT_LIMIT = 10 * 1024;            // 10 KB per invocation

// ── Simple LRU code cache ──

interface CachedFunction {
  code: string;
  env_vars: Record<string, string>;
  cachedAt: number;
}

const codeCache = new Map<string, CachedFunction>();

function getCached(key: string): CachedFunction | null {
  const entry = codeCache.get(key);
  if (!entry) return null;
  if (Date.now() - entry.cachedAt > CODE_CACHE_TTL) {
    codeCache.delete(key);
    return null;
  }
  return entry;
}

function setCache(key: string, fn: CachedFunction): void {
  // Evict oldest if at capacity
  if (codeCache.size >= CODE_CACHE_SIZE) {
    const oldest = codeCache.keys().next().value;
    if (oldest) codeCache.delete(oldest);
  }
  codeCache.set(key, fn);
}

// ── Concurrency limiter ──

let activeConcurrency = 0;

// ── Worker bootstrap ──
//
// Loaded once at startup. Each invocation creates a per-tenant Web
// Worker from this code with `permissions: 'none'` — no net, no env, no
// read/write/run/ffi. User JS runs there in isolation; capability calls
// (DB, vault, log) are proxied back to this parent process via
// postMessage.
//
// Closes GHSA-7428-mvpp-rhr7 layer 2.
const BOOTSTRAP_PATH = new URL("./worker_bootstrap.js", import.meta.url);
let bootstrapBlobUrl: string | null = null;

async function getBootstrapBlobUrl(): Promise<string> {
  if (bootstrapBlobUrl) return bootstrapBlobUrl;
  const code = await Deno.readTextFile(BOOTSTRAP_PATH);
  const blob = new Blob([code], { type: "application/javascript" });
  bootstrapBlobUrl = URL.createObjectURL(blob);
  return bootstrapBlobUrl;
}

// ── Database connection ──

// Use dynamic import for postgres — Deno supports it natively via deno.land/x.
// The runner role (eurobase_function_runner) has membership in every
// per-tenant `<schema>_func` role but no direct grants on tenant schemas
// or `public.*` (beyond a couple of helper functions). Per-invocation
// code wraps every query in a transaction with `SET LOCAL ROLE` so the
// SQL runs with the executing tenant's grants only.
// deno-lint-ignore no-explicit-any
let sql: any = null;

async function getDB() {
  if (sql) return sql;
  const { default: postgres } = await import("https://deno.land/x/postgresjs@v3.4.4/mod.js");
  sql = postgres(DB_URL, { max: 10 });
  return sql;
}

// ── Load function code ──

async function loadFunction(functionId: string): Promise<CachedFunction | null> {
  const cached = getCached(functionId);
  if (cached) return cached;

  try {
    const db = await getDB();
    // compiled_code is the esbuild artifact the gateway produces on
    // deploy (TS stripped, ESM -> CommonJS, closes #189). NULL means
    // the function predates the transpile step — fall back to the raw
    // source, which for those functions is plain JS by contract.
    const [row] = await db`
      SELECT COALESCE(compiled_code, code) AS code, COALESCE(env_vars, '{}') as env_vars
      FROM edge_functions
      WHERE id = ${functionId} AND status = 'active'
    `;
    if (!row) return null;

    const fn: CachedFunction = {
      code: row.code,
      env_vars: typeof row.env_vars === "string" ? JSON.parse(row.env_vars) : row.env_vars,
      cachedAt: Date.now(),
    };
    setCache(functionId, fn);
    return fn;
  } catch (err) {
    console.error("Failed to load function:", err);
    return null;
  }
}

// ── Log capture with truncation ──

function createLogCapture(projectId: string) {
  let totalBytes = 0;
  const logs: string[] = [];
  let truncated = false;

  function capture(level: string, msg: string, data?: Record<string, unknown>) {
    if (truncated) return;
    const line = `[fn:${projectId}] ${level}: ${msg}${data ? " " + JSON.stringify(data) : ""}`;
    const lineBytes = new TextEncoder().encode(line).length;
    if (totalBytes + lineBytes > LOG_OUTPUT_LIMIT) {
      truncated = true;
      logs.push(`[fn:${projectId}] WARN: Log output truncated at ${LOG_OUTPUT_LIMIT} bytes`);
      return;
    }
    totalBytes += lineBytes;
    logs.push(line);
    // Still emit to server console
    if (level === "ERROR") console.error(line);
    else if (level === "WARN") console.warn(line);
    else console.log(line);
  }

  return {
    info: (msg: string, data?: Record<string, unknown>) => capture("INFO", msg, data),
    warn: (msg: string, data?: Record<string, unknown>) => capture("WARN", msg, data),
    error: (msg: string, data?: Record<string, unknown>) => capture("ERROR", msg, data),
    getLogs: () => logs,
  };
}

// ── Execute function ──

async function executeFunction(
  fn: CachedFunction,
  req: Request,
  headers: Headers,
): Promise<Response> {
  const projectId = headers.get("X-Project-ID") ?? "";
  const schemaName = headers.get("X-Schema-Name") ?? "";
  const userId = headers.get("X-User-ID") ?? "";
  const userEmail = headers.get("X-User-Email") ?? "";
  const plan = headers.get("X-Plan") ?? "free";
  const requestId = headers.get("X-Request-ID") ?? crypto.randomUUID();

  // Determine timeout based on plan.
  const timeoutMs = plan === "pro" ? 60_000 : 10_000;

  // Defence-in-depth: validate schema name shape before it reaches SQL.
  // The gateway already validates this, but the runner is on the cluster
  // network and a future misconfiguration could expose it more broadly
  // (these headers are HMAC-verified in authenticateRequest below; this
  // shape check is the second layer).
  if (!validIdentRe.test(schemaName)) {
    return jsonResponse({ error: "invalid schema name", requestId }, 400);
  }
  const funcRole = tenantFuncRole(schemaName);
  if (!validIdentRe.test(funcRole)) {
    // Belt-and-braces: tenantFuncRole() should never return invalid since
    // schemaName already passed validIdentRe, but hard-fail if it does.
    return jsonResponse({ error: "invalid tenant role", requestId }, 400);
  }

  // The per-invocation Web Worker (created below) runs user JS with
  // `permissions: 'none'`. DB / vault calls come back to this parent
  // over postMessage and run here under the per-tenant role.
  const setRoleSQL = "SET LOCAL ROLE " + quoteIdent(funcRole);
  const setPathSQL = "SET LOCAL search_path TO " + quoteIdent(schemaName);
  const db = await getDB();
  const logCapture = createLogCapture(projectId);

  // deno-lint-ignore no-explicit-any
  async function runDBSql(query: string, params: unknown[]): Promise<any> {
    // deno-lint-ignore no-explicit-any
    return await db.begin(async (tx: any) => {
      await tx.unsafe(setRoleSQL);
      await tx.unsafe(setPathSQL);
      // Mirror the gateway's RLS context so auth_uid() /
      // is_service_role() behave the same in functions as in gateway
      // REST — see rlsContextStatements in role.ts. Closes #188.
      for (const stmt of rlsContextStatements(userId)) {
        await tx.unsafe(stmt.sql, stmt.params);
      }
      return await tx.unsafe(query, params);
    });
  }

  // Materialise the request body up front. The worker can't stream
  // across the postMessage boundary, and we already enforce a 5MB
  // request-size limit upstream, so a single Uint8Array is fine.
  let bodyBytes: Uint8Array | null = null;
  if (req.body) {
    bodyBytes = new Uint8Array(await req.arrayBuffer());
  }
  const serializedRequest: SerializedRequest = {
    method: req.method,
    url: req.url,
    headers: [...req.headers.entries()],
    body: bodyBytes,
  };

  const bootstrapUrl = await getBootstrapBlobUrl();
  // Worker capability boundaries — closes #83. We allow outbound `net`
  // because edge functions need to call external APIs (Stripe, OpenAI,
  // fal.ai, etc.); cluster-internal egress (Postgres, gateway, kube-dns
  // beyond UDP/53) is blocked by the k8s NetworkPolicy on the functions
  // Deployment, so user JS still can't reach internal services. Every
  // other permission is explicitly denied to keep credential isolation
  // intact (env was the original load-bearing property — sandbox_test
  // still asserts Deno.env is unreadable).
  //
  // Defaults of unspecified keys would inherit from the parent process,
  // which has --allow-env / --allow-read=/app — so we list every
  // permission explicitly and never rely on defaults.
  const worker = new Worker(bootstrapUrl, {
    type: "module",
    deno: {
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

  return await runUserHandlerInWorker({
    worker,
    fn,
    requestId,
    projectId,
    schemaName,
    userId,
    serializedRequest,
    user: userId ? { id: userId, email: userEmail } : null,
    timeoutMs,
    runDBSql,
    db,
    logCapture,
  });
}

// runUserHandlerInWorker drives the load → invoke → result lifecycle for
// a single Worker. Wires up the postMessage RPC for db.sql / vault.get /
// log, enforces the timeout, and tears the worker down on every exit.
async function runUserHandlerInWorker(opts: {
  worker: Worker;
  fn: CachedFunction;
  requestId: string;
  projectId: string;
  schemaName: string;
  userId: string;
  serializedRequest: SerializedRequest;
  user: { id: string; email: string } | null;
  timeoutMs: number;
  runDBSql: (query: string, params: unknown[]) => Promise<unknown>;
  // deno-lint-ignore no-explicit-any
  db: any;
  // deno-lint-ignore no-explicit-any
  logCapture: ReturnType<typeof createLogCapture>;
}): Promise<Response> {
  const {
    worker,
    fn,
    requestId,
    projectId,
    schemaName,
    userId,
    serializedRequest,
    user,
    timeoutMs,
    runDBSql,
    db,
    logCapture,
  } = opts;
  const storageCtx = { projectID: projectId, schemaName, userID: userId };

  let timeoutTimer: number | undefined;
  let settled = false;

  return new Promise<Response>((resolve) => {
    const cleanup = () => {
      if (settled) return;
      settled = true;
      if (timeoutTimer !== undefined) clearTimeout(timeoutTimer);
      try {
        worker.terminate();
      } catch (_) {
        // worker may already be gone.
      }
    };

    const respondError = (status: number, message: string) => {
      cleanup();
      resolve(jsonResponse({ error: message, requestId }, status));
    };

    const respondResponse = (serialized: SerializedResponse) => {
      cleanup();
      if (serialized.body.byteLength > RESPONSE_SIZE_LIMIT) {
        resolve(jsonResponse({
          error: "Response too large",
          limit: `${RESPONSE_SIZE_LIMIT / 1024 / 1024}MB`,
          requestId,
        }, 413));
        return;
      }
      const headers = new Headers();
      for (const [k, v] of serialized.headers) headers.set(k, v);
      headers.set("X-Request-ID", requestId);
      resolve(new Response(serialized.body, {
        status: serialized.status,
        headers,
      }));
    };

    timeoutTimer = setTimeout(() => {
      respondError(504, "Function timed out");
    }, timeoutMs);

    worker.addEventListener("error", (e: ErrorEvent) => {
      console.error(`[fn:${projectId}] Worker error:`, e.message);
      respondError(500, e.message || "worker error");
    });

    worker.addEventListener("message", (event: MessageEvent) => {
      if (settled) return;
      const msg = event.data as WorkerToParent;
      if (!msg || typeof msg.type !== "string") return;
      switch (msg.type) {
        case "loaded": {
          const invokeMsg: ParentToWorker = {
            type: "invoke",
            request: serializedRequest,
            env: fn.env_vars,
            user,
            requestId,
            timeoutMs,
          };
          worker.postMessage(invokeMsg);
          break;
        }
        case "result":
          respondResponse(msg.response);
          break;
        case "error":
          respondError(500, msg.message);
          break;
        case "log":
          if (msg.level === "ERROR") logCapture.error(msg.msg, msg.data as Record<string, unknown> | undefined);
          else if (msg.level === "WARN") logCapture.warn(msg.msg, msg.data as Record<string, unknown> | undefined);
          else logCapture.info(msg.msg, msg.data as Record<string, unknown> | undefined);
          break;
        case "db.sql.call": {
          // Run the query under the per-tenant role and post the
          // result back. Errors are reported as `error` strings so the
          // worker's RPC layer can rebuild an Error.
          runDBSql(msg.query, msg.params)
            .then((rows) => {
              if (settled) return;
              const reply: ParentToWorker = { type: "db.sql.result", id: msg.id, rows };
              worker.postMessage(reply);
            })
            .catch((err) => {
              if (settled) return;
              const text = err instanceof Error ? err.message : String(err);
              const reply: ParentToWorker = { type: "db.sql.result", id: msg.id, error: text };
              worker.postMessage(reply);
            });
          break;
        }
        case "vault.get.call": {
          // Closes #79: read the encrypted blob via the SECURITY DEFINER
          // helper public.vault_get_for_runner (added by migration 000049)
          // and decrypt locally with VAULT_ENCRYPTION_KEY. Failure modes
          // (missing secret, no encryption key, decrypt failure) all
          // resolve to null so the worker contract stays `string | null`.
          resolveVaultSecret(db, projectId, msg.name)
            .then((value) => {
              if (settled) return;
              const reply: ParentToWorker = { type: "vault.get.result", id: msg.id, value };
              worker.postMessage(reply);
            })
            .catch((err) => {
              if (settled) return;
              console.error(`[fn:${projectId}] vault.get failed`, err);
              const reply: ParentToWorker = { type: "vault.get.result", id: msg.id, value: null };
              worker.postMessage(reply);
            });
          break;
        }
        case "storage.upload.call": {
          // Closes #85. POST the bytes to the gateway's HMAC-protected
          // /internal/functions/storage/upload. Errors come back as
          // `{ error: string }` so the worker's RPC layer rejects the
          // user-side Promise; success delivers `{ key, size }`.
          uploadObject(storageCtx, msg.key, msg.body, msg.contentType ?? "application/octet-stream")
            .then((result) => {
              if (settled) return;
              const reply: ParentToWorker = { type: "storage.upload.result", id: msg.id, ...result };
              worker.postMessage(reply);
            });
          break;
        }
        case "storage.signed_url.call": {
          createSignedUrl(storageCtx, msg.key, msg.operation, {
            expiresIn: msg.expiresIn,
            contentType: msg.contentType,
          }).then((result) => {
            if (settled) return;
            const reply: ParentToWorker = { type: "storage.signed_url.result", id: msg.id, ...result };
            worker.postMessage(reply);
          });
          break;
        }
        case "storage.delete.call": {
          deleteObject(storageCtx, msg.key).then((result) => {
            if (settled) return;
            const reply: ParentToWorker = { type: "storage.delete.result", id: msg.id, ...result };
            worker.postMessage(reply);
          });
          break;
        }
      }
    });

    // First message: ship user code so the worker can load it via
    // Function constructor. Doing this on a tick separation ensures
    // the message-event listener above is fully wired before we send.
    queueMicrotask(() => {
      const loadMsg: ParentToWorker = { type: "load", code: fn.code };
      worker.postMessage(loadMsg);
    });
  });
}

// ── Helpers ──

function jsonResponse(body: Record<string, unknown>, status: number): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

// ── HMAC verifier (lazy-initialised) ──
//
// Constructed once on the first authenticated request. We don't await
// the import key at module-load so a transient secret-typo crash
// surfaces as a 500 on the first request, not at process boot — the
// pod stays running and operators can fix the env var without rolling
// the deployment.
let _verifier: Verifier | null = null;
let _verifierError: Error | null = null;

async function getVerifier(): Promise<Verifier | null> {
  if (_verifier) return _verifier;
  if (_verifierError) return null;
  if (!HMAC_SECRET) return null;
  try {
    _verifier = await newVerifier(HMAC_SECRET);
    return _verifier;
  } catch (err) {
    _verifierError = err instanceof Error ? err : new Error(String(err));
    console.error(
      "FATAL: failed to initialise HMAC verifier — gateway → runner traffic is unauthenticated",
      _verifierError,
    );
    return null;
  }
}

// authenticateRequest verifies the HMAC signature on /invoke. Behaviour:
//   - Strict mode (HMAC_REQUIRE_SIGNED=true) and HMAC_SECRET set:
//     returns 401 on missing or bad signature.
//   - Soft mode (default) and HMAC_SECRET set:
//     missing signature → warn + accept; bad signature → 401.
//   - HMAC_SECRET unset:
//     warn + accept (rollout window only).
//
// Returns null on success, a Response on rejection.
async function authenticateRequest(req: Request, requestId: string): Promise<Response | null> {
  if (!HMAC_SECRET) {
    if (HMAC_REQUIRE_SIGNED) {
      console.error(
        "rejecting unsigned request: FUNCTIONS_RUNNER_HMAC_REQUIRE_SIGNED=true but HMAC_SECRET is empty",
      );
      return jsonResponse({ error: "runner misconfigured", requestId }, 500);
    }
    console.warn("HMAC_SECRET not set — accepting unsigned request (rollout-window soft mode)");
    return null;
  }
  const verifier = await getVerifier();
  if (!verifier) {
    return jsonResponse({ error: "runner misconfigured", requestId }, 500);
  }
  const result = await verifier.verify(req.headers);
  if (result.ok) return null;

  if (result.reason === "missing" && !HMAC_REQUIRE_SIGNED) {
    console.warn("accepting unsigned request (soft mode); set FUNCTIONS_RUNNER_HMAC_REQUIRE_SIGNED=true to enforce");
    return null;
  }
  console.warn(`rejecting request: ${result.reason}`);
  return jsonResponse({ error: "unauthorized", reason: result.reason, requestId }, 401);
}

// ── HTTP Server ──

Deno.serve({ port: PORT }, async (req: Request): Promise<Response> => {
  const url = new URL(req.url);

  // Health check — never authenticated.
  if (url.pathname === "/health") {
    return jsonResponse({
      status: "ok",
      active: activeConcurrency,
      cached: codeCache.size,
    }, 200);
  }

  // Function invocation.
  if (url.pathname === "/invoke") {
    const functionId = req.headers.get("X-Function-ID");
    const requestId = req.headers.get("X-Request-ID") ?? crypto.randomUUID();
    const plan = req.headers.get("X-Plan") ?? "free";

    // Authenticate before doing any work.
    const rejection = await authenticateRequest(req, requestId);
    if (rejection) return rejection;

    if (!functionId) {
      return jsonResponse({ error: "Missing X-Function-ID header", requestId }, 400);
    }

    // Enforce request body size limit.
    const contentLength = parseInt(req.headers.get("Content-Length") ?? "0");
    const maxRequestSize = plan === "pro" ? REQUEST_SIZE_PRO : REQUEST_SIZE_FREE;
    if (contentLength > maxRequestSize) {
      return jsonResponse({
        error: "Request body too large",
        limit: `${maxRequestSize / 1024 / 1024}MB`,
        requestId,
      }, 413);
    }

    // Concurrency check.
    if (activeConcurrency >= MAX_CONCURRENT) {
      return jsonResponse({ error: "Too many concurrent executions", requestId }, 429);
    }

    // Load function code.
    const fn = await loadFunction(functionId);
    if (!fn) {
      return jsonResponse({ error: "Function not found or disabled", requestId }, 404);
    }

    // Execute.
    activeConcurrency++;
    try {
      return await executeFunction(fn, req, req.headers);
    } finally {
      activeConcurrency--;
    }
  }

  return jsonResponse({ error: "Not found" }, 404);
});

console.log(`Eurobase Function Runner listening on port ${PORT}`);
