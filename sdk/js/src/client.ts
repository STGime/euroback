/**
 * Main client factory for the Eurobase SDK.
 */

import { AuthClient } from './auth'
import { DatabaseClient } from './database'
import { StorageClient } from './storage'
import { RealtimeClient } from './realtime'
import { httpClient } from './http'

// ---------------------------------------------------------------------------
// Public types
// ---------------------------------------------------------------------------

/** Configuration required to initialise the Eurobase client. */
export interface EurobaseConfig {
  /** Base URL of the Eurobase gateway (e.g. "https://api.eurobase.eu"). */
  url: string
  /** Project API key used for authentication. */
  apiKey: string
}

/** The top-level Eurobase client with database, storage, realtime, and auth access. */
export interface EurobaseClient {
  /** End-user authentication. */
  auth: AuthClient
  /** Database query builder. */
  db: DatabaseClient
  /** Object storage operations. */
  storage: StorageClient
  /** Realtime subscriptions via WebSocket. */
  realtime: RealtimeClient
  /** Check connectivity to the Eurobase gateway. */
  status(): Promise<{ ok: boolean; latency_ms: number }>
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
    db: new DatabaseClient(config),
    storage: new StorageClient(config),
    realtime: new RealtimeClient(config),
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
  }
}
