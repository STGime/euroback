/**
 * Functions client for invoking Edge Functions.
 *
 * Edge Functions are serverless TypeScript/JavaScript functions that run
 * on Eurobase's EU-sovereign infrastructure.
 */

import type { EurobaseConfig, HttpClient } from './http'

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
  private http: HttpClient

  constructor(config: EurobaseConfig, http: HttpClient) {
    this.http = http
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
    const path = `/v1/functions/${functionName}`

    try {
      const result = await this.http.post(path, options?.body)

      if (result.error) {
        return {
          data: null,
          error: { status: 0, message: result.error },
        }
      }

      return { data: result as T, error: null }
    } catch (err) {
      return {
        data: null,
        error: { status: 0, message: (err as Error).message },
      }
    }
  }
}
