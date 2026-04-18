/**
 * Realtime client for Eurobase.
 * Connects via WebSocket to /v1/realtime and subscribes to database change
 * events (INSERT, UPDATE, DELETE) on specific tables.
 *
 * Uses the native WebSocket API (browsers and Node.js 21+).
 */

import type { EurobaseConfig, HttpClient } from './http'

// ---------------------------------------------------------------------------
// Public types
// ---------------------------------------------------------------------------

/** A change event received over the realtime channel. */
export interface RealtimeEvent {
  channel: string
  type: string
  record: any
  old_record?: any
  timestamp: string
}

/** Callback invoked when a matching event arrives. */
export type SubscriptionCallback = (event: RealtimeEvent) => void

/** Handle for a single subscription — pass to `off()` to unsubscribe. */
export interface Subscription {
  id: string
  table: string
  event: string
  callback: SubscriptionCallback
}

/** Event types that can be subscribed to. */
export type RealtimeEventType = 'INSERT' | 'UPDATE' | 'DELETE' | '*'

// ---------------------------------------------------------------------------
// RealtimeClient
// ---------------------------------------------------------------------------

/** Client that manages WebSocket subscriptions to Eurobase change events. */
export class RealtimeClient {
  private config: EurobaseConfig
  private http: HttpClient
  private ws: WebSocket | null = null
  private subscriptions: Map<string, Subscription> = new Map()
  private nextId = 1
  private reconnectAttempts = 0
  private maxReconnectDelay = 30_000 // 30 seconds
  private shouldReconnect = true
  private pendingSubscribes: string[] = []

  constructor(config: EurobaseConfig, http: HttpClient) {
    this.config = config
    this.http = http
  }

  /**
   * Subscribe to events on a table.
   * The WebSocket connection is established lazily on the first subscription.
   */
  on(
    table: string,
    event: RealtimeEventType,
    callback: SubscriptionCallback,
  ): Subscription {
    const id = `sub_${this.nextId++}`
    const sub: Subscription = { id, table, event, callback }
    this.subscriptions.set(id, sub)

    // Ensure connected, then send the subscribe message.
    this.ensureConnected()

    const channel = `db:${table}`
    if (this.ws && this.ws.readyState === WebSocket.OPEN) {
      this.sendSubscribe(channel)
    } else {
      // Queue it — will be sent once the socket opens.
      this.pendingSubscribes.push(channel)
    }

    return sub
  }

  /** Remove a subscription. If no subscriptions remain, the socket stays open. */
  off(subscription: Subscription): void {
    this.subscriptions.delete(subscription.id)
  }

  /** Close the WebSocket connection and clear all subscriptions. */
  disconnect(): void {
    this.shouldReconnect = false
    this.subscriptions.clear()
    this.pendingSubscribes = []
    if (this.ws) {
      this.ws.close()
      this.ws = null
    }
  }

  // ---- Internal -----------------------------------------------------------

  private ensureConnected(): void {
    if (this.ws && (this.ws.readyState === WebSocket.OPEN || this.ws.readyState === WebSocket.CONNECTING)) {
      return
    }
    this.connect()
  }

  private connect(): void {
    const baseUrl = this.config.url.replace(/\/+$/, '').replace(/^http/, 'ws')
    const token = this.http.getAccessToken() || this.config.apiKey
    const url = `${baseUrl}/v1/realtime?token=${encodeURIComponent(token)}`

    this.ws = new WebSocket(url)

    this.ws.onopen = () => {
      this.reconnectAttempts = 0

      // Send any queued subscribe messages.
      const channels = new Set(this.pendingSubscribes)
      this.pendingSubscribes = []
      for (const ch of channels) {
        this.sendSubscribe(ch)
      }

      // Re-subscribe all existing subscriptions (in case of reconnect).
      const activeChannels = new Set<string>()
      for (const sub of this.subscriptions.values()) {
        activeChannels.add(`db:${sub.table}`)
      }
      for (const ch of activeChannels) {
        this.sendSubscribe(ch)
      }
    }

    this.ws.onmessage = (messageEvent: MessageEvent) => {
      try {
        const event: RealtimeEvent = JSON.parse(
          typeof messageEvent.data === 'string' ? messageEvent.data : '',
        )
        this.routeEvent(event)
      } catch {
        // Ignore malformed messages.
      }
    }

    this.ws.onclose = () => {
      this.ws = null
      if (this.shouldReconnect && this.subscriptions.size > 0) {
        this.scheduleReconnect()
      }
    }

    this.ws.onerror = () => {
      // The close handler will fire after an error, triggering reconnect.
    }
  }

  private sendSubscribe(channel: string): void {
    if (this.ws && this.ws.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify({ action: 'subscribe', channel }))
    }
  }

  private routeEvent(event: RealtimeEvent): void {
    for (const sub of this.subscriptions.values()) {
      const channel = `db:${sub.table}`
      if (event.channel !== channel) continue
      if (sub.event === '*' || sub.event === event.type) {
        try {
          sub.callback(event)
        } catch {
          // Swallow errors in user callbacks to keep the loop going.
        }
      }
    }
  }

  /** Reconnect with exponential backoff: 1s, 2s, 4s, ... up to 30s. */
  private scheduleReconnect(): void {
    const delay = Math.min(
      1000 * Math.pow(2, this.reconnectAttempts),
      this.maxReconnectDelay,
    )
    this.reconnectAttempts++
    setTimeout(() => {
      if (this.shouldReconnect && this.subscriptions.size > 0) {
        this.connect()
      }
    }, delay)
  }
}
