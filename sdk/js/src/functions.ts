/**
 * Functions client for invoking Edge Functions.
 *
 * Edge Functions are serverless TypeScript/JavaScript functions that run
 * on Eurobase's EU-sovereign infrastructure.
 */

import type { EurobaseConfig } from './http'

export interface FunctionInvokeOptions {
  /** Request body (will be JSON-stringified). */
  body?: unknown
  /** HTTP method (defaults to POST). */
  method?: 'GET' | 'POST' | 'PUT' | 'DELETE'
  /** Additional headers. */
  headers?: Record<string, string>
}

export interface FunctionError {
  status: number
  message: string
}

export class FunctionsClient {
  private config: EurobaseConfig

  constructor(config: EurobaseConfig) {
    this.config = config
  }

  private get baseUrl(): string {
    return this.config.url.replace(/\/+$/, '')
  }

  private defaultHeaders(): Record<string, string> {
    return {
      'apikey': this.config.apiKey,
      'Content-Type': 'application/json',
    }
  }

  /**
   * Invoke an edge function by name.
   *
   * @example
   * ```ts
   * const { data, error } = await eurobase.functions.invoke('process-order', {
   *   body: { orderId: 'abc-123' },
   * })
   * ```
   */
  async invoke<T = unknown>(
    functionName: string,
    options?: FunctionInvokeOptions,
  ): Promise<{ data: T | null; error: FunctionError | null }> {
    const method = options?.method ?? 'POST'
    const url = `${this.baseUrl}/v1/functions/${functionName}`

    try {
      const res = await fetch(url, {
        method,
        headers: {
          ...this.defaultHeaders(),
          ...options?.headers,
        },
        body: options?.body ? JSON.stringify(options.body) : undefined,
      })

      if (!res.ok) {
        const body = await res.json().catch(() => ({ error: 'Unknown error' }))
        return {
          data: null,
          error: { status: res.status, message: body?.error || `HTTP ${res.status}` },
        }
      }

      const contentType = res.headers.get('content-type') || ''
      if (contentType.includes('application/json')) {
        const data = await res.json()
        return { data: data as T, error: null }
      }

      // Non-JSON response — return as text.
      const text = await res.text()
      return { data: text as unknown as T, error: null }
    } catch (err) {
      return {
        data: null,
        error: { status: 0, message: (err as Error).message },
      }
    }
  }
}
