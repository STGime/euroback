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
import { privilegeStatementError, uuidRe } from "./sql_guard.ts";
import { isLoginError, TenantDBPool } from "./tenant_db.ts";
import { functionCacheKey } from "./cache_key.ts";
import type {
  ParentToWorker,
  SerializedRequest,
  SerializedResponse,
  WorkerToParent,
} from "./bridge.ts";
import { newVerifier, type Verifier } from "./hmac.ts";
import { openSealed, resolveVaultSecret } from "./vault.ts";
import { createSignedUrl, deleteObject, uploadObject } from "./storage.ts";
import { createLogCapture, encodeLogLinesHeader } from "./logs.ts";

// Closes GHSA-7428-mvpp-rhr7 layer 1: the runner's own login
// (`eurobase_function_runner`) has no grants on any tenant schema and,
// since stage B phase 1b, no membership in any tenant role. Customer SQL
// runs on a separate connection that logs in as the tenant's own
// `<schema>_func` role (see tenant_db.ts).
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
// The shared connection (eurobase_function_runner) is used only for the
// platform lookups: runner_get_function and vault_get_for_runner.
// deno-lint-ignore no-explicit-any
let sql: any = null;

// Per-tenant logins (stage B): ctx.db.sql runs on a connection
// authenticated as the tenant's `<schema>_func` role. Requires
// FUNC_PASSWORD_SECRET; without it ctx.db.sql fails.
const FUNC_PASSWORD_SECRET = Deno.env.get("FUNC_PASSWORD_SECRET") ?? "";
// Per pod; see the connection budget note on the HPA in deploy/k8s/functions.yaml.
const TENANT_CONN_CAP = parseInt(Deno.env.get("RUNNER_TENANT_CONN_CAP") ?? "10");
let tenantPool: TenantDBPool | null = null;

async function getTenantPool(): Promise<TenantDBPool | null> {
  if (FUNC_PASSWORD_SECRET.length < 32 || !DB_URL) return null;
  if (tenantPool) return tenantPool;
  const { default: postgres } = await import("https://deno.land/x/postgresjs@v3.4.4/mod.js");
  tenantPool = new TenantDBPool(
    DB_URL,
    FUNC_PASSWORD_SECRET,
    (url: string) => postgres(url, { max: 1, idle_timeout: 30 }),
    TENANT_CONN_CAP,
  );
  return tenantPool;
}

async function getDB() {
  if (sql) return sql;
  const { default: postgres } = await import("https://deno.land/x/postgresjs@v3.4.4/mod.js");
  sql = postgres(DB_URL, { max: 3 }); // platform lookups only
  return sql;
}

// ── Load function code ──

async function loadFunction(
  functionId: string,
  projectId: string,
  version: string | null = null,
): Promise<CachedFunction | null> {
  // Key by project+id+version so a redeploy is effective immediately
  // instead of after TTL expiry (see cache_key.ts, closes #200), and so a
  // cached entry is only ever served to the project it belongs to.
  const cacheKey = functionCacheKey(`${projectId}:${functionId}`, version);
  const cached = getCached(cacheKey);
  if (cached) return cached;

  try {
    const db = await getDB();
    // compiled_code is the esbuild artifact the gateway produces on
    // deploy (TS stripped, ESM -> CommonJS, closes #189). NULL means
    // the function predates the transpile step — fall back to the raw
    // source, which for those functions is plain JS by contract.
    //
    // env_vars at rest (#206): the legacy `env_vars` JSONB is plaintext
    // and being phased out; new writes land in (env_vars_blob,
    // env_vars_nonce, env_vars_key_version) sealed with the per-tenant
    // HKDF-derived AES-256-GCM key. We JOIN projects to get
    // schema_name (the HKDF salt — same one functions-runner/vault.ts
    // already uses for ctx.vault.get).
    //
    // Loaded through public.runner_get_function (migrations 000125/000126):
    // a SECURITY DEFINER lookup scoped to the calling project. The runner
    // role has no direct access to platform tables.
    const [row] = await db`
      SELECT code, env_vars_legacy, env_vars_blob, env_vars_nonce,
             env_vars_key_version, schema_name
        FROM public.runner_get_function(${functionId}::uuid, ${projectId}::uuid)
    `;
    if (!row) return null;

    const env = await resolveEnvVars(row);

    const fn: CachedFunction = {
      code: row.code,
      env_vars: env,
      cachedAt: Date.now(),
    };
    setCache(cacheKey, fn);
    return fn;
  } catch (err) {
    console.error("Failed to load function:", err);
    return null;
  }
}

// resolveEnvVars implements the read-path documented in migration 000067:
// sealed columns take precedence; legacy plaintext is the lazy-migration
// fallback; both absent is the empty map.
//
// A decryption failure is loudly logged and downgraded to an empty map.
// We deliberately do NOT fall through to legacy plaintext here — a
// developer who set env vars and saw them sealed shouldn't see them
// silently disappear and we shouldn't paper over a key-mismatch by
// reading the column we promised was deprecated.
async function resolveEnvVars(row: {
  env_vars_legacy: unknown;
  env_vars_blob: Uint8Array | null;
  env_vars_nonce: Uint8Array | null;
  env_vars_key_version: number | null;
  schema_name: string;
}): Promise<Record<string, string>> {
  const { env_vars_blob: blob, env_vars_nonce: nonce, env_vars_key_version: keyVer, schema_name: schemaName } = row;

  if (blob && nonce && keyVer != null) {
    try {
      const plaintext = await openSealed(schemaName, blob, nonce, keyVer);
      return JSON.parse(plaintext);
    } catch (err) {
      console.error(
        "[fn] env_vars decrypt failed — running with empty env until key is healed",
        err instanceof Error ? err.message : err,
      );
      return {};
    }
  }

  const legacy = row.env_vars_legacy;
  if (legacy == null) return {};
  if (typeof legacy === "string") {
    try {
      return JSON.parse(legacy) as Record<string, string>;
    } catch {
      return {};
    }
  }
  return legacy as Record<string, string>;
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
  const setPathSQL = "SET LOCAL search_path TO " + quoteIdent(schemaName);
  const db = await getDB();
  const logCapture = createLogCapture(projectId, LOG_OUTPUT_LIMIT);

  // deno-lint-ignore no-explicit-any
  async function runDBSql(query: string, params: unknown[]): Promise<any> {
    // Customer SQL must not change the role or search_path set below.
    const guardErr = privilegeStatementError(query);
    if (guardErr) throw new Error(guardErr);

    // Statement timeout per transaction, matching the invocation budget,
    // so a query from an invocation that already timed out can't keep a
    // per-tenant connection busy.
    const timeoutSQL = `SET LOCAL statement_timeout = ${timeoutMs}`;

    // Customer SQL only ever runs on a connection that logs in as the
    // tenant's own `<schema>_func` role (stage B phase 1b). There is no
    // shared-connection fallback: the runner's own login is no longer a
    // member of any tenant role, and falling back is exactly the path a
    // tenant could steer into to reach another tenant.
    const tp = await getTenantPool();
    if (!tp) {
      throw new Error("database access is not configured on this runner (FUNC_PASSWORD_SECRET)");
    }
    let started = false;
    let lease;
    try {
      lease = await tp.acquire(schemaName, funcRole);
      // deno-lint-ignore no-explicit-any
      return await lease.client.begin(async (tx: any) => {
        started = true;
        await tx.unsafe(timeoutSQL);
        await tx.unsafe(setPathSQL);
        // Mirror the gateway's RLS context so auth_uid() /
        // is_service_role() behave the same in functions as in gateway
        // REST — see rlsContextStatements in role.ts. Closes #188.
        for (const stmt of rlsContextStatements(userId)) {
          await tx.unsafe(stmt.sql, stmt.params);
        }
        return await tx.unsafe(query, params);
      });
    } catch (err) {
      // A failed login (not applied yet for a brand-new project) gets a
      // retryable message and a fresh client next time. Errors raised by
      // the customer's SQL — even with an auth-like SQLSTATE — pass
      // through unchanged.
      if (started || !isLoginError(err)) throw err;
      lease?.invalidate();
      console.warn(`[tenant-db] login for ${funcRole} failed (${(err as { code?: string }).code})`);
      throw new Error("database login for this project is unavailable; retry shortly");
    } finally {
      lease?.release();
    }
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

    // Attaches captured log lines to the response header the
    // gateway parses in HandleInvoke to persist into
    // edge_function_logs.log_lines. Header omitted when the
    // invocation emitted no ctx.log.* calls (nothing to persist).
    // Base64 because header values must be ASCII-safe and log
    // messages/data are user-supplied UTF-8.
    const attachLogsHeader = (headers: Headers) => {
      const encoded = encodeLogLinesHeader(logCapture.getLines());
      if (encoded) headers.set("X-Function-Logs", encoded);
    };

    const respondError = (status: number, message: string) => {
      cleanup();
      const errResponse = jsonResponse({ error: message, requestId }, status);
      attachLogsHeader(errResponse.headers);
      resolve(errResponse);
    };

    const respondResponse = (serialized: SerializedResponse) => {
      cleanup();
      if (serialized.body.byteLength > RESPONSE_SIZE_LIMIT) {
        const tooLarge = jsonResponse({
          error: "Response too large",
          limit: `${RESPONSE_SIZE_LIMIT / 1024 / 1024}MB`,
          requestId,
        }, 413);
        attachLogsHeader(tooLarge.headers);
        resolve(tooLarge);
        return;
      }
      const headers = new Headers();
      for (const [k, v] of serialized.headers) headers.set(k, v);
      headers.set("X-Request-ID", requestId);
      attachLogsHeader(headers);
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
          // helper public.vault_get_for_runner (migration 000049,
          // key_version added in 000061) and decrypt locally with the
          // key for the version the secret was sealed with (#201).
          // Missing secret → null; platform-side failures (no key,
          // lookup/decrypt failure) → error, which the worker's RPC
          // layer surfaces to user code as a thrown Error so "secret
          // missing" and "vault broken" are distinguishable.
          resolveVaultSecret(db, projectId, schemaName, msg.name)
            .then((result) => {
              if (settled) return;
              if ("error" in result) {
                logCapture.warn(`vault.get(${JSON.stringify(msg.name)}) failed: ${result.error}`);
                const reply: ParentToWorker = { type: "vault.get.result", id: msg.id, value: null, error: result.error };
                worker.postMessage(reply);
                return;
              }
              const reply: ParentToWorker = { type: "vault.get.result", id: msg.id, value: result.value };
              worker.postMessage(reply);
            })
            .catch((err) => {
              if (settled) return;
              console.error(`[fn:${projectId}] vault.get failed`, err);
              const reply: ParentToWorker = { type: "vault.get.result", id: msg.id, value: null, error: "vault unavailable: internal error" };
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
    const functionVersion = req.headers.get("X-Function-Version");
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
    const projectIdHeader = req.headers.get("X-Project-ID") ?? "";
    if (!uuidRe.test(projectIdHeader)) {
      return jsonResponse({ error: "invalid project id", requestId }, 400);
    }
    const fn = await loadFunction(functionId, projectIdHeader, functionVersion);
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
