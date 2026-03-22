/**
 * Main client factory for the Eurobase SDK.
 */

import { DatabaseClient } from './database'
import { StorageClient } from './storage'
import { RealtimeClient } from './realtime'

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

/** The top-level Eurobase client with database, storage, and realtime access. */
export interface EurobaseClient {
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
 * // Query the database
 * const { data, error } = await eurobase.db
 *   .from('users')
 *   .select('id', 'name', 'email')
 *   .eq('status', 'active')
 *   .order('created_at', { ascending: false })
 *   .limit(20)
 *
 * // Check connectivity
 * const { ok, latency_ms } = await eurobase.status()
 *
 * // Upload a file
 * const result = await eurobase.storage.upload('avatar.png', file)
 *
 * // Listen for realtime changes
 * eurobase.realtime.on('messages', 'INSERT', (event) => {
 *   console.log('New message:', event.record)
 * })
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

  return {
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
