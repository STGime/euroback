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
 * const eurobase = createClient({
 *   url: 'https://api.eurobase.eu',
 *   apiKey: 'eb_live_...',
 * })
 *
 * // Query the database
 * const { data, error } = await eurobase.db
 *   .from('users')
 *   .select('id', 'name', 'email')
 *   .eq('status', 'active')
 *   .order('created_at', { ascending: false })
 *   .limit(20)
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
export function createClient(config: EurobaseConfig): EurobaseClient {
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
  }
}
