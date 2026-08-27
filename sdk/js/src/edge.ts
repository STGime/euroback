/**
 * TypeScript types for the Eurobase Edge Function runtime.
 *
 * These types describe the shape of the `req` and `ctx` arguments the
 * Eurobase functions-runner passes to a deployed handler. They ship
 * from `@eurobase/sdk/functions` as a **types-only** subpath — no
 * runtime code is emitted, and this file must never depend on any of
 * the other SDK modules (they target the browser / Node client
 * surface, which is a different environment from the edge runtime).
 *
 * Import in your function source with a `type-only` import so bundlers
 * elide it cleanly at build time:
 *
 * ```ts
 * import type { EdgeHandler, EdgeContext } from '@eurobase/sdk/functions'
 *
 * const handler: EdgeHandler = async (req, ctx) => {
 *   const { orderId } = (await req.json()) as { orderId: string }
 *   const { rows } = await ctx.db.sql<{ id: string; total: number }>(
 *     'SELECT id, total FROM orders WHERE id = $1',
 *     [orderId],
 *   )
 *   return Response.json(rows[0])
 * }
 * export default handler
 * ```
 *
 * Types track the shape emitted by the functions-runner
 * (`functions-runner/worker_bootstrap.js` — `makeCtx()`) and are kept
 * in sync with it on every SDK release. If the runtime adds a new
 * `ctx.*` helper, this file gains a matching field in the same
 * release; the version alignment is what makes the subpath approach
 * better than an out-of-tree `.d.ts` copy for consumers.
 */

/**
 * End-user identity, populated by the gateway for `verify_jwt` function
 * routes from the validated end-user JWT. `null` when the function was
 * invoked without an end-user JWT (public routes, service-key calls,
 * webhook receivers, etc.).
 */
export interface EdgeUser {
  /** End-user UUID from the tenant's own `users` table. */
  id: string
  /** End-user email if present on the JWT. Optional because some
   *  JWT shapes (phone-auth, magic-link) omit it. */
  email?: string
}

/**
 * Result of `ctx.db.sql(...)`. `TRow` narrows the row shape so
 * `rows[0].foo` typechecks in the caller:
 *
 * ```ts
 * const { rows } = await ctx.db.sql<{ id: string }>('SELECT id FROM t')
 * ```
 */
export interface EdgeQueryResult<TRow = Record<string, unknown>> {
  /** Result rows in query order. Empty array for statements that
   *  return no rows (INSERT/UPDATE/DELETE without RETURNING). */
  rows: TRow[]
  /** Number of rows affected (INSERT/UPDATE/DELETE) or returned
   *  (SELECT with RETURNING). Matches `pg`'s command tag. */
  rowCount: number
}

/**
 * Payload from `ctx.storage.upload(...)`.
 */
export interface EdgeStorageUploadResult {
  /** Object key in tenant object storage — echoed from the upload call. */
  key: string
  /** Size of the uploaded object in bytes. */
  size: number
  /** MIME type stored on the object (from `opts.contentType`, or
   *  auto-detected server-side when omitted). */
  contentType: string
  /** Server-generated ETag; useful for `If-Match` / `If-None-Match`. */
  etag: string
}

/**
 * Payload from `ctx.storage.createSignedUrl(...)`.
 */
export interface EdgeSignedUrlResult {
  /** Time-limited signed URL. Handle in the caller's browser or
   *  return to the end-user; the URL is scoped to the operation
   *  (`upload` or `download`) and expires. */
  url: string
  /** ISO-8601 timestamp when the URL stops being valid. */
  expiresAt: string
}

/**
 * Body types accepted by `ctx.storage.upload(...)`. Uint8Array is the
 * primary shape (structured-cloneable across the runner bridge); the
 * others are converted transparently.
 */
export type EdgeStorageUploadBody = Uint8Array | ArrayBuffer | Blob | string

/**
 * Options for `ctx.storage.upload(...)`.
 */
export interface EdgeStorageUploadOptions {
  /** MIME type to store on the object. If omitted, the server
   *  auto-detects from the body bytes / filename. */
  contentType?: string
}

/**
 * Options for `ctx.storage.createSignedUrl(...)`.
 */
export interface EdgeStorageSignedUrlOptions {
  /** URL lifetime in seconds. Defaults to 300 (5 minutes) — cap is
   *  set by the plan (7 days on Pro/Team). */
  expiresIn?: number
  /** MIME type expected on the eventual PUT (only for `operation:
   *  'upload'`). Signed into the URL — a client uploading with a
   *  different Content-Type gets a 403 from object storage. */
  contentType?: string
}

/**
 * Structured-log surface. Every call emits a JSON line the console's
 * log viewer picks up under the invocation's `requestId`. Prefer this
 * over `console.log` (which is available but goes to the runner's
 * stderr, not the per-invocation trace).
 *
 * `data` is arbitrary JSON — objects, arrays, primitives. Deeply
 * nested values are logged as-is; very large payloads may be truncated
 * at ingest by the audit-log ingestion path.
 */
export interface EdgeLogger {
  info(msg: string, data?: unknown): void
  warn(msg: string, data?: unknown): void
  error(msg: string, data?: unknown): void
}

/**
 * The context object every edge function receives as its second
 * argument. Corresponds to what the runner's `makeCtx()` builds per
 * invocation.
 */
export interface EdgeContext {
  /**
   * Postgres query surface. Runs against the project's tenant schema
   * with RLS applied — the same policies your SDK / REST callers see.
   */
  db: {
    /**
     * Execute a parameterised SQL query. Values in `params` bind to
     * `$1`, `$2`, … placeholders; do not string-interpolate values
     * into `query` (SQL injection).
     */
    sql<TRow = Record<string, unknown>>(
      query: string,
      params?: unknown[],
    ): Promise<EdgeQueryResult<TRow>>
  }

  /**
   * Encrypted secret store. Returns the plaintext value for a secret
   * name previously set from the console or via
   * `eb.vault.set(name, value)` in the platform SDK. Throws if the
   * secret does not exist for this project.
   */
  vault: {
    get(name: string): Promise<string>
  }

  /**
   * Storage helpers that speak to the tenant's S3-compatible object
   * store. All operations are scoped to the invoking project.
   */
  storage: {
    upload(
      key: string,
      body: EdgeStorageUploadBody,
      opts?: EdgeStorageUploadOptions,
    ): Promise<EdgeStorageUploadResult>
    createSignedUrl(
      key: string,
      operation: 'upload' | 'download',
      opts?: EdgeStorageSignedUrlOptions,
    ): Promise<EdgeSignedUrlResult>
    delete(key: string): Promise<{ deleted: boolean }>
  }

  /**
   * Read-only view of the function's configured environment variables
   * (set via the console's Function → Environment tab). Prefer
   * `ctx.vault.get(...)` for anything sensitive — env vars are
   * process-visible; vault reads are individually audit-logged.
   */
  env: Readonly<Record<string, string>>

  /**
   * End-user identity for `verify_jwt` function routes; `null` on
   * public / service-key / webhook invocations.
   */
  user: EdgeUser | null

  /**
   * Per-invocation trace ID. Included in every audit-log entry the
   * function produces and returned to the caller in `X-Request-ID`.
   * Use it as a correlation key when you fan out to external APIs.
   */
  requestId: string

  /**
   * Structured logger. See `EdgeLogger` for details.
   */
  log: EdgeLogger
}

/**
 * The shape every deployed handler must satisfy. Default-export a
 * function of this type from your function source:
 *
 * ```ts
 * const handler: EdgeHandler = async (req, ctx) => { ... }
 * export default handler
 * ```
 *
 * `req` is a standard `Request` (from the runner's Web Fetch surface).
 * The return value can be a `Response` or any JSON-serialisable value;
 * non-Response returns are wrapped as
 * `new Response(JSON.stringify(value), { status: 200, headers: { 'Content-Type': 'application/json' } })`
 * by the runner before being handed back to the caller.
 */
export type EdgeHandler = (
  req: Request,
  ctx: EdgeContext,
) => Response | Promise<Response> | unknown | Promise<unknown>
