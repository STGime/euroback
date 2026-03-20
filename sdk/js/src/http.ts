/**
 * Internal HTTP client used by database and storage sub-clients.
 * Uses the native fetch() API — works in browsers and Node.js 18+.
 */

export interface EurobaseConfig {
  url: string
  apiKey: string
}

export interface HttpClient {
  get(path: string, params?: Record<string, string>): Promise<any>
  post(path: string, body?: any): Promise<any>
  patch(path: string, body?: any): Promise<any>
  del(path: string): Promise<any>
  postForm(path: string, formData: FormData): Promise<any>
  getBlob(path: string): Promise<Blob>
}

/**
 * Creates an HTTP client that sends all requests to the Eurobase gateway
 * with the appropriate Authorization header.
 */
export function httpClient(config: EurobaseConfig): HttpClient {
  const baseUrl = config.url.replace(/\/+$/, '')

  function buildUrl(path: string, params?: Record<string, string>): string {
    let url = `${baseUrl}${path}`
    if (params && Object.keys(params).length > 0) {
      const qs = new URLSearchParams(params).toString()
      url += `?${qs}`
    }
    return url
  }

  function headers(contentType?: string): Record<string, string> {
    const h: Record<string, string> = {
      'Authorization': `Bearer ${config.apiKey}`,
    }
    if (contentType) {
      h['Content-Type'] = contentType
    }
    return h
  }

  async function handleResponse(res: Response): Promise<any> {
    if (res.status === 204) {
      return { error: null }
    }

    const contentType = res.headers.get('content-type') || ''
    if (!contentType.includes('application/json')) {
      if (!res.ok) {
        return { error: `HTTP ${res.status}: ${res.statusText}` }
      }
      return { error: null }
    }

    const data = await res.json()

    if (!res.ok) {
      const errorMessage = data?.error || `HTTP ${res.status}: ${res.statusText}`
      return { error: errorMessage }
    }

    return data
  }

  return {
    async get(path: string, params?: Record<string, string>): Promise<any> {
      try {
        const res = await fetch(buildUrl(path, params), {
          method: 'GET',
          headers: headers(),
        })
        return handleResponse(res)
      } catch (err) {
        return { error: (err as Error).message }
      }
    },

    async post(path: string, body?: any): Promise<any> {
      try {
        const res = await fetch(buildUrl(path), {
          method: 'POST',
          headers: headers('application/json'),
          body: body !== undefined ? JSON.stringify(body) : undefined,
        })
        return handleResponse(res)
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
        return handleResponse(res)
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
        return handleResponse(res)
      } catch (err) {
        return { error: (err as Error).message }
      }
    },

    async postForm(path: string, formData: FormData): Promise<any> {
      try {
        // Do not set Content-Type — the browser/runtime sets it with
        // the correct multipart boundary automatically.
        const res = await fetch(buildUrl(path), {
          method: 'POST',
          headers: {
            'Authorization': `Bearer ${config.apiKey}`,
          },
          body: formData,
        })
        return handleResponse(res)
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
