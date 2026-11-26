/**
 * Internal HTTP client used by database and storage sub-clients.
 * Uses the native fetch() API — works in browsers and Node.js 18+.
 */

export interface EurobaseConfig {
  url: string
  apiKey: string
  /** Project UUID (optional; required for end-user JWT realtime). See
   * client.ts for the user-facing docs. */
  projectId?: string
  /** Session handling of `auth` (server mode: `{ persistSession: false }`). */
  auth?: EurobaseAuthOptions
  /** Auth-email language (`"de"`, `"auto"`, …). See client.ts. */
  locale?: string
}

/** Session handling of the client's `auth`. */
export interface EurobaseAuthOptions {
  /**
   * Keep the signed-in user's session on this client (default `true`).
   *
   * `true`: after a sign-in the client stores the session (localStorage
   * in a browser, memory elsewhere) and sends that user's access token on
   * every later `db` / `storage` / `functions` / `realtime` call.
   *
   * `false` (server mode): the client keeps **no** session at all — not in
   * storage, not in memory. `signIn` / `signUp` / `refreshSession(token)`
   * still return the session, but the client never sends it and emits no
   * auth events. Use this for a client shared by the requests of many
   * users (a Node / Deno / Bun / edge server), and act as a user with
   * `withAccessToken(token)`. Unlike supabase-js, where
   * `persistSession: false` still keeps the session in memory.
   */
  persistSession?: boolean
  /**
   * Refresh the stored session shortly before its access token expires
   * (default `true`). Only applies with `persistSession: true`.
   */
  autoRefreshToken?: boolean
}

/**
 * Query parameters. The pair-array form lets a key repeat — needed for
 * several filters on one column, e.g. a date range
 * (`created_at=gte.A&created_at=lte.B`).
 */
export type QueryParams = Record<string, string> | Array<[string, string]>

/** Per-request options (internal). */
export interface RequestOptions {
  /** Abort the request after this many milliseconds (an error result). */
  timeoutMs?: number
  /** Let the request finish even if the page navigates away. */
  keepalive?: boolean
  /** Send this end-user access token instead of the client's own. */
  accessToken?: string
}

export interface HttpClient {
  get(path: string, params?: QueryParams, options?: RequestOptions): Promise<any>
  post(path: string, body?: any, options?: RequestOptions): Promise<any>
  patch(path: string, body?: any): Promise<any>
  del(path: string): Promise<any>
  postForm(path: string, formData: FormData): Promise<any>
  getBlob(path: string): Promise<Blob>
  setAccessToken(token: string | null): void
  getAccessToken(): string | null
}

/**
 * Creates an HTTP client that sends all requests to the Eurobase gateway
 * with the appropriate apikey header and optional Authorization Bearer token.
 */
export function httpClient(config: EurobaseConfig): HttpClient {
  const baseUrl = config.url.replace(/\/+$/, '')
  let accessToken: string | null = null

  function buildUrl(path: string, params?: QueryParams): string {
    let url = `${baseUrl}${path}`
    const qs = params ? new URLSearchParams(params).toString() : ''
    if (qs) {
      url += `?${qs}`
    }
    return url
  }

  function headers(contentType?: string, options?: RequestOptions): Record<string, string> {
    const h: Record<string, string> = {
      'apikey': config.apiKey,
    }
    const token = options?.accessToken ?? accessToken
    if (token) {
      h['Authorization'] = `Bearer ${token}`
    }
    if (contentType) {
      h['Content-Type'] = contentType
    }
    return h
  }

  /**
   * Error results carry `status`: the HTTP status of an error response.
   * It is absent when no response arrived (network error, offline) or a
   * success body couldn't be read.
   */
  async function handleResponse(res: Response): Promise<any> {
    if (res.status === 204) {
      return { error: null }
    }

    const contentType = res.headers.get('content-type') || ''
    if (!contentType.includes('application/json')) {
      if (!res.ok) {
        return { error: `HTTP ${res.status}: ${res.statusText}`, status: res.status }
      }
      return { error: null }
    }

    if (!res.ok) {
      // An error page with a JSON content type but a broken body still
      // reports its status.
      const data = await res.json().catch(() => null)
      const errorMessage = data?.error || `HTTP ${res.status}: ${res.statusText}`
      const out: { error: string; status: number; code?: string } = { error: errorMessage, status: res.status }
      // Machine-readable code from the gateway, when it sends one (e.g.
      // the Postgres error code of a failed RPC call).
      if (typeof data?.code === 'string') out.code = data.code
      return out
    }

    const data = await res.json()

    return data
  }

  return {
    setAccessToken(token: string | null) {
      accessToken = token
    },

    getAccessToken() {
      return accessToken
    },

    async get(path: string, params?: QueryParams, options?: RequestOptions): Promise<any> {
      try {
        const res = await fetch(buildUrl(path, params), {
          method: 'GET',
          headers: headers(undefined, options),
        })
        // `await` here, so a body that fails to parse lands in the catch
        // below instead of rejecting the caller (#814).
        return await handleResponse(res)
      } catch (err) {
        return { error: (err as Error).message }
      }
    },

    async post(path: string, body?: any, options?: RequestOptions): Promise<any> {
      try {
        const init: RequestInit = {
          method: 'POST',
          headers: headers('application/json', options),
          body: body !== undefined ? JSON.stringify(body) : undefined,
        }
        if (options?.keepalive) init.keepalive = true
        if (options?.timeoutMs && typeof AbortSignal !== 'undefined' && typeof AbortSignal.timeout === 'function') {
          init.signal = AbortSignal.timeout(options.timeoutMs)
        }
        const res = await fetch(buildUrl(path), init)
        // `await` here, so a body that fails to parse lands in the catch
        // below instead of rejecting the caller (#814).
        return await handleResponse(res)
      } catch (err) {
        return { error: (err as Error).message }
      }
    },

    async patch(path: string, body?: any): Promise<any> {
      try {
        const res = await fetch(buildUrl(path), {
          method: 'PATCH',
          headers: headers('application/json'),
          body: body !== undefined ? JSON.stringify(body) : undefined,
        })
        // `await` here, so a body that fails to parse lands in the catch
        // below instead of rejecting the caller (#814).
        return await handleResponse(res)
      } catch (err) {
        return { error: (err as Error).message }
      }
    },

    async del(path: string): Promise<any> {
      try {
        const res = await fetch(buildUrl(path), {
          method: 'DELETE',
          headers: headers(),
        })
        // `await` here, so a body that fails to parse lands in the catch
        // below instead of rejecting the caller (#814).
        return await handleResponse(res)
      } catch (err) {
        return { error: (err as Error).message }
      }
    },

    async postForm(path: string, formData: FormData): Promise<any> {
      try {
        // Do not set Content-Type — the browser/runtime sets it with
        // the correct multipart boundary automatically.
        const h: Record<string, string> = {
          'apikey': config.apiKey,
        }
        if (accessToken) {
          h['Authorization'] = `Bearer ${accessToken}`
        }
        const res = await fetch(buildUrl(path), {
          method: 'POST',
          headers: h,
          body: formData,
        })
        // `await` here, so a body that fails to parse lands in the catch
        // below instead of rejecting the caller (#814).
        return await handleResponse(res)
      } catch (err) {
        return { error: (err as Error).message }
      }
    },

    async getBlob(path: string): Promise<Blob> {
      const res = await fetch(buildUrl(path), {
        method: 'GET',
        headers: headers(),
      })
      if (!res.ok) {
        throw new Error(`HTTP ${res.status}: ${res.statusText}`)
      }
      return res.blob()
    },
  }
}
