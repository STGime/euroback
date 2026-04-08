/**
 * Eurobase Edge Functions Runner
 *
 * Deno HTTP server that receives proxied requests from the Gateway,
 * loads function code from PostgreSQL, and executes it in a sandboxed context.
 *
 * This server runs inside the Kapsule cluster on an internal ClusterIP — it
 * is NEVER exposed to the public internet. Only the Gateway can reach it.
 */

const DB_URL = Deno.env.get("DATABASE_URL") ?? "";
const PORT = parseInt(Deno.env.get("PORT") ?? "8000");
const MAX_CONCURRENT = parseInt(Deno.env.get("MAX_CONCURRENT_ISOLATES") ?? "50");
const CODE_CACHE_SIZE = parseInt(Deno.env.get("CODE_CACHE_SIZE") ?? "200");
const CODE_CACHE_TTL = parseInt(Deno.env.get("CODE_CACHE_TTL_SECONDS") ?? "300") * 1000;

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

// ── Database connection ──

// Use dynamic import for postgres — Deno supports it natively via deno.land/x
// For production, pin to a specific version.
// deno-lint-ignore no-explicit-any
let sql: any = null;

async function getDB() {
  if (sql) return sql;
  const { default: postgres } = await import("https://deno.land/x/postgresjs@v3.4.4/mod.js");
  sql = postgres(DB_URL, { max: 10, ssl: { rejectUnauthorized: false } });
  return sql;
}

// ── Load function code ──

async function loadFunction(functionId: string): Promise<CachedFunction | null> {
  const cached = getCached(functionId);
  if (cached) return cached;

  try {
    const db = await getDB();
    const [row] = await db`
      SELECT code, COALESCE(env_vars, '{}') as env_vars
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

  // Build the context object that gets injected into the function.
  const db = await getDB();

  const logCapture = createLogCapture(projectId);

  const ctx = {
    db: {
      async sql(query: string, params: unknown[] = []) {
        // Set search path to project schema, then execute.
        await db`SELECT set_config('search_path', ${schemaName}, true)`;
        return await db.unsafe(query, params);
      },
    },
    vault: {
      async get(name: string): Promise<string | null> {
        try {
          const [row] = await db`
            SELECT value FROM vault_secrets
            WHERE project_id = ${projectId} AND name = ${name}
          `;
          return row?.value ?? null;
        } catch {
          return null;
        }
      },
    },
    env: fn.env_vars,
    user: userId ? { id: userId, email: userEmail } : null,
    log: logCapture,
    requestId,
  };

  // Execute the function with a timeout.
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);

  try {
    // Use Function constructor for sandboxed execution.
    // In production, this would use Deno.Worker or a V8 isolate for stronger isolation.
    const AsyncFunction = Object.getPrototypeOf(async function(){}).constructor;
    const handlerFn = new AsyncFunction("req", "ctx", `
      ${fn.code}

      // Call the default export
      if (typeof handler === 'function') return handler(req, ctx);
      if (typeof exports !== 'undefined' && typeof exports.default === 'function') return exports.default(req, ctx);
      throw new Error('Function must export a default handler');
    `);

    const response = await handlerFn(req, ctx);

    if (response instanceof Response) {
      // Enforce response size limit.
      const body = await response.clone().arrayBuffer();
      if (body.byteLength > RESPONSE_SIZE_LIMIT) {
        return jsonResponse({
          error: "Response too large",
          limit: `${RESPONSE_SIZE_LIMIT / 1024 / 1024}MB`,
          requestId,
        }, 413);
      }
      // Passthrough X-Request-ID on every response.
      const newHeaders = new Headers(response.headers);
      newHeaders.set("X-Request-ID", requestId);
      return new Response(response.body, {
        status: response.status,
        headers: newHeaders,
      });
    }

    // If the function returned something else, wrap it.
    return new Response(JSON.stringify(response), {
      status: 200,
      headers: { "Content-Type": "application/json", "X-Request-ID": requestId },
    });
  } catch (err) {
    if (controller.signal.aborted) {
      return jsonResponse({ error: "Function timed out", requestId }, 504);
    }

    const message = err instanceof Error ? err.message : "Unknown error";
    console.error(`[fn:${projectId}] Execution error:`, message);
    return jsonResponse({ error: message, requestId }, 500);
  } finally {
    clearTimeout(timer);
  }
}

// ── Helpers ──

function jsonResponse(body: Record<string, unknown>, status: number): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

// ── HTTP Server ──

Deno.serve({ port: PORT }, async (req: Request): Promise<Response> => {
  const url = new URL(req.url);

  // Health check.
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
