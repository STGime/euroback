/**
 * Main client factory for the Eurobase SDK.
 */

import { AuthClient } from './auth'
import { DatabaseClient } from './database'
import { FunctionsClient } from './functions'
import { StorageClient } from './storage'
import { RealtimeClient } from './realtime'
import { VaultClient } from './vault'
import { httpClient } from './http'
import type { EurobaseAuthOptions } from './http'
import { stripBearer } from './jwt'

export type { EurobaseAuthOptions } from './http'

// ---------------------------------------------------------------------------
// Public types
// ---------------------------------------------------------------------------

/** Configuration required to initialise the Eurobase client. */
export interface EurobaseConfig {
  /** Base URL of the Eurobase gateway (e.g. "https://api.eurobase.eu"). */
  url: string
  /** Project API key used for authentication. */
  apiKey: string
  /**
   * Project UUID. Only required if you call `realtime.*` with an
   * end-user access token (e.g. after `auth.signIn`) — the gateway
   * needs the project ID up front to validate the JWT against the
   * project's secret. For API-key-only realtime usage it can be
   * omitted; the gateway derives the project from the key.
   *
   * Connection strings of the form
   * `eurobase://API_KEY@SLUG.eurobase.app` do NOT populate this — set
   * it explicitly if you need end-user JWT realtime.
   *
   * `auth.verifyAccessToken()` also checks a token's audience against it.
   */
  projectId?: string
  /**
   * Session handling. On a server that handles requests of several users,
   * use `{ persistSession: false }` (server mode: the client keeps no
   * session) and `withAccessToken(token)` per request.
   */
  auth?: EurobaseAuthOptions
  /**
   * Language of the auth emails (verification, password reset, magic link)
   * this client asks for: a language tag such as `"de"` or `"nb-NO"`, or
   * `"auto"` for the device's language (`navigator.language`, else the
   * `Intl` locale) in a browser or React Native app. On a server `"auto"`
   * sends nothing (the server's language isn't the user's); pass the
   * user's language per call there. Sent as `locale` with
   * `signUp`, `resendVerification`, `forgotPassword` and `requestMagicLink`;
   * a `locale` in a call's options wins. Omitted: nothing is sent and the
   * project's default language applies. Supported: en, de, fr, nl, sv, es,
   * nb (others fall back to the project default).
   */
  locale?: string
}

/**
 * A client that acts as one end user: every `db`, `storage`, `functions`
 * and `realtime` call sends that user's access token, so RLS applies as
 * that user. Made by `withAccessToken()`; it shares nothing mutable with
 * other clients and keeps no session.
 */
export interface EurobaseUserClient {
  /** The user's own account calls (`getUser()`, `updatePassword()`, …). */
  auth: AuthClient
  db: DatabaseClient
  functions: FunctionsClient
  storage: StorageClient
  /** Call `realtime.disconnect()` when you are done with a subscription. */
  realtime: RealtimeClient
  /** The access token this client sends. */
  readonly accessToken: string
}

/** The top-level Eurobase client with database, storage, realtime, and auth access. */
export interface EurobaseClient {
  /** End-user authentication. */
  auth: AuthClient
  /** Database query builder. */
  db: DatabaseClient
  /** Edge Functions invocation. */
  functions: FunctionsClient
  /** Object storage operations. */
  storage: StorageClient
  /** Realtime subscriptions via WebSocket. */
  realtime: RealtimeClient
  /** Encrypted secrets vault (requires secret API key). */
  vault: VaultClient
  /** Check connectivity to the Eurobase gateway. */
  status(): Promise<{ ok: boolean; latency_ms: number }>
  /**
   * A lightweight client that acts as the end user whose access token you
   * pass (e.g. from the `Authorization` header your server received).
   * Create one per request; RLS applies as that user. It does not check
   * the token — verify it first with `auth.verifyAccessToken()`; the
   * gateway refuses an invalid one (401) either way.
   *
   * Needs a client created with the **public** key: with the secret key
   * the gateway runs every call as the service role, which skips RLS, so
   * this throws.
   */
  withAccessToken(accessToken: string): EurobaseUserClient
}

// ---------------------------------------------------------------------------
// Connection string parser
// ---------------------------------------------------------------------------

/**
 * Parse a connection string in the format:
 *   eurobase://API_KEY@SLUG.eurobase.app
 *
 * Returns an EurobaseConfig object.
 */
function parseConnectionString(connStr: string): EurobaseConfig {
  // Remove the eurobase:// prefix
  const withoutScheme = connStr.replace(/^eurobase:\/\//, '')
  const atIndex = withoutScheme.indexOf('@')
  if (atIndex === -1) {
    throw new Error('Eurobase: invalid connection string — expected eurobase://API_KEY@host')
  }

  const apiKey = withoutScheme.slice(0, atIndex)
  const host = withoutScheme.slice(atIndex + 1)

  if (!apiKey || !host) {
    throw new Error('Eurobase: invalid connection string — API key and host are required')
  }

  return {
    url: `https://${host}`,
    apiKey,
  }
}

// ---------------------------------------------------------------------------
// Factory
// ---------------------------------------------------------------------------

/**
 * Create a new Eurobase client.
 *
 * @example
 * ```ts
 * import { createClient } from '@eurobase/sdk'
 *
 * // Object config
 * const eurobase = createClient({
 *   url: 'https://my-app.eurobase.app',
 *   apiKey: 'eb_pk_...',
 * })
 *
 * // Connection string (single-string format)
 * const eurobase = createClient('eurobase://eb_pk_xxx@my-app.eurobase.app')
 *
 * // Authenticate an end-user
 * const { data, error } = await eurobase.auth.signIn({
 *   email: 'user@example.com',
 *   password: 'password123',
 * })
 *
 * // Query the database (scoped to authenticated user via RLS)
 * const { data, error } = await eurobase.db
 *   .from('todos')
 *   .select('id', 'title', 'completed')
 *   .eq('completed', 'false')
 *   .order('created_at', { ascending: false })
 *   .limit(20)
 *
 * // Check connectivity
 * const { ok, latency_ms } = await eurobase.status()
 * ```
 */
export function createClient(configOrConnectionString: EurobaseConfig | string): EurobaseClient {
  let config: EurobaseConfig

  if (typeof configOrConnectionString === 'string') {
    config = parseConnectionString(configOrConnectionString)
  } else {
    config = configOrConnectionString
  }

  if (!config.url) {
    throw new Error('Eurobase: url is required')
  }
  if (!config.apiKey) {
    throw new Error('Eurobase: apiKey is required')
  }

  const http = httpClient(config)
  const authClient = new AuthClient(config, http)

  return {
    auth: authClient,
    db: new DatabaseClient(config, http),
    functions: new FunctionsClient(config, http),
    storage: new StorageClient(config, http),
    realtime: new RealtimeClient(config, http),
    vault: new VaultClient(config),
    async status() {
      const start = Date.now()
      try {
        const res = await fetch(`${config.url}/health`)
        const latency_ms = Date.now() - start
        return { ok: res.ok, latency_ms }
      } catch {
        const latency_ms = Date.now() - start
        return { ok: false, latency_ms }
      }
    },
    withAccessToken(accessToken: string): EurobaseUserClient {
      const token = typeof accessToken === 'string' ? stripBearer(accessToken) : ''
      if (!token) {
        throw new Error('Eurobase: withAccessToken() needs an access token')
      }
      if (config.apiKey.startsWith('eb_sk_')) {
        throw new Error(
          'Eurobase: withAccessToken() needs a client created with the public key (eb_pk_…). ' +
          'With the secret key every call runs as the service role and RLS does not apply.',
        )
      }
      // Own config copy and HTTP client: nothing here is shared with the
      // parent client or other requests except the JWKS cache.
      const userConfig = { ...config, auth: { persistSession: false, autoRefreshToken: false } }
      const userHttp = httpClient(userConfig)
      userHttp.setAccessToken(token)
      return {
        auth: new AuthClient(userConfig, userHttp, { verifier: authClient.accessTokenVerifier }),
        db: new DatabaseClient(userConfig, userHttp),
        functions: new FunctionsClient(userConfig, userHttp),
        storage: new StorageClient(userConfig, userHttp),
        realtime: new RealtimeClient(userConfig, userHttp, { fixedToken: true }),
        accessToken: token,
      }
    },
  }
}
