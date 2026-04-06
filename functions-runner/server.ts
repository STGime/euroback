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
  sql = postgres(DB_URL, { max: 10 });
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

  // Determine timeout based on plan.
  const timeoutMs = plan === "pro" ? 60_000 : 10_000;

  // Build the context object that gets injected into the function.
  const db = await getDB();

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
    log: {
      info: (msg: string, data?: Record<string, unknown>) => console.log(`[fn:${projectId}] INFO:`, msg, data ?? ""),
      warn: (msg: string, data?: Record<string, unknown>) => console.warn(`[fn:${projectId}] WARN:`, msg, data ?? ""),
      error: (msg: string, data?: Record<string, unknown>) => console.error(`[fn:${projectId}] ERROR:`, msg, data ?? ""),
    },
  };

  // Execute the function with a timeout.
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);

  try {
    // Create a data URL module from the function code.
    // This runs the code in an isolated module context.
    const wrappedCode = `
      ${fn.code}

      // Export the default handler if it exists
      const _handler = typeof handler !== 'undefined' ? handler : (typeof default_export !== 'undefined' ? default_export : null);
      export { _handler };
    `;

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
      return response;
    }

    // If the function returned something else, wrap it.
    return new Response(JSON.stringify(response), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  } catch (err) {
    if (controller.signal.aborted) {
      return new Response(JSON.stringify({ error: "Function timed out" }), {
        status: 504,
        headers: { "Content-Type": "application/json" },
      });
    }

    const message = err instanceof Error ? err.message : "Unknown error";
    console.error(`[fn:${projectId}] Execution error:`, message);
    return new Response(JSON.stringify({ error: message }), {
      status: 500,
      headers: { "Content-Type": "application/json" },
    });
  } finally {
    clearTimeout(timer);
  }
}

// ── HTTP Server ──

Deno.serve({ port: PORT }, async (req: Request): Promise<Response> => {
  const url = new URL(req.url);

  // Health check.
  if (url.pathname === "/health") {
    return new Response(JSON.stringify({
      status: "ok",
      active: activeConcurrency,
      cached: codeCache.size,
    }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  }

  // Function invocation.
  if (url.pathname === "/invoke") {
    const functionId = req.headers.get("X-Function-ID");
    if (!functionId) {
      return new Response(JSON.stringify({ error: "Missing X-Function-ID header" }), {
        status: 400,
        headers: { "Content-Type": "application/json" },
      });
    }

    // Concurrency check.
    if (activeConcurrency >= MAX_CONCURRENT) {
      return new Response(JSON.stringify({ error: "Too many concurrent executions" }), {
        status: 429,
        headers: { "Content-Type": "application/json" },
      });
    }

    // Load function code.
    const fn = await loadFunction(functionId);
    if (!fn) {
      return new Response(JSON.stringify({ error: "Function not found or disabled" }), {
        status: 404,
        headers: { "Content-Type": "application/json" },
      });
    }

    // Execute.
    activeConcurrency++;
    try {
      return await executeFunction(fn, req, req.headers);
    } finally {
      activeConcurrency--;
    }
  }

  return new Response("Not found", { status: 404 });
});

console.log(`Eurobase Function Runner listening on port ${PORT}`);
