/**
 * User admin API — `eb.auth.admin` — for your **server**.
 *
 * Manages the project's end users (create, list, update, delete, suspend,
 * set a password, sign out everywhere) through `/v1/auth/admin/users`, like
 * the console's Users page. It needs the project's **secret** key
 * (`eb_sk_…`); the gateway refuses every other key with 403. Never ship the
 * secret key to a browser or an app.
 *
 * ```ts
 * const eb = createClient({ url: process.env.EUROBASE_URL!, apiKey: process.env.EUROBASE_SECRET_KEY! })
 * const { data: user, error } = await eb.auth.admin.createUser({
 *   email: 'ana@partner.example',
 *   emailConfirmed: true,
 *   metadata: { company: 'Partner GmbH' },
 * })
 * ```
 *
 * Requests carry only the API key: a signed-in end user's access token on
 * the same client is never sent, and would change nothing.
 */

import type { EurobaseConfig } from './http'

/** An end user as the admin API returns it (no password or token data). */
export interface AdminUser {
  id: string
  email: string | null
  phone?: string | null
  display_name: string | null
  /** Sign-in methods linked to the account, e.g. `["email"]`, `["google"]`. */
  providers: string[]
  metadata: Record<string, unknown>
  /** Set while the user is suspended. */
  banned_at: string | null
  last_sign_in_at: string | null
  /** Null until the email address is confirmed. */
  email_confirmed_at: string | null
  created_at: string
}

/** One page of users, newest first. */
export interface AdminUserList {
  users: AdminUser[]
  /** Users matching the search (all pages). */
  total: number
}

export interface AdminListUsersOptions {
  /** 1-based page number (default 1). */
  page?: number
  /** Users per page, 1–200 (default 50). */
  perPage?: number
  /** Case-insensitive substring of the email, display name or id. */
  search?: string
}

export interface AdminCreateUserParams {
  email: string
  /** 8–72 bytes. Omit it for users who sign in by magic link, OAuth or a password reset. */
  password?: string
  /** Default `true`: you vouch for the address. `false` leaves it unconfirmed. */
  emailConfirmed?: boolean
  displayName?: string
  metadata?: Record<string, unknown>
}

export interface AdminUpdateUserParams {
  email?: string
  displayName?: string
  /** Replaces the whole metadata object. */
  metadata?: Record<string, unknown>
  /** `true` confirms the email (keeps an existing date), `false` marks it unconfirmed. */
  emailConfirmed?: boolean
}

/** Every admin method resolves to this; it never throws. */
export interface AdminResult<T> {
  data: T | null
  error: string | null
  /** HTTP status of an error response (absent without a response). */
  status?: number
  /** Machine-readable code when the gateway sends one, e.g. `email_exists`, `secret_key_required`. */
  code?: string
}

/** The error for a client created with a public key: no request is sent. */
export const ADMIN_NEEDS_SECRET_KEY =
  "auth.admin needs the project's secret key (eb_sk_...): use it on your server only, never in a browser or an app"

export class AdminClient {
  private config: EurobaseConfig

  constructor(config: EurobaseConfig) {
    this.config = config
  }

  private get baseUrl(): string {
    return this.config.url.replace(/\/+$/, '') + '/v1/auth/admin/users'
  }

  private async request<T>(method: string, path: string, body?: unknown): Promise<AdminResult<T>> {
    if (!this.config.apiKey?.startsWith('eb_sk_')) {
      return { data: null, error: ADMIN_NEEDS_SECRET_KEY, status: 403, code: 'secret_key_required' }
    }
    try {
      const headers: Record<string, string> = { apikey: this.config.apiKey }
      if (body !== undefined) headers['Content-Type'] = 'application/json'
      const res = await fetch(this.baseUrl + path, {
        method,
        headers,
        body: body !== undefined ? JSON.stringify(body) : undefined,
      })
      if (res.status === 204) {
        return { data: null, error: null }
      }
      const text = await res.text()
      let json: any = null
      try {
        json = text ? JSON.parse(text) : null
      } catch {
        json = null
      }
      if (!res.ok) {
        const out: AdminResult<T> = {
          data: null,
          error: (json && typeof json.error === 'string' && json.error) || `HTTP ${res.status}: ${res.statusText}`,
          status: res.status,
        }
        if (json && typeof json.code === 'string') out.code = json.code
        return out
      }
      return { data: json as T, error: null }
    } catch (err) {
      return { data: null, error: (err as Error).message }
    }
  }

  private userPath(id: string, suffix = ''): string {
    return '/' + encodeURIComponent(id) + suffix
  }

  /** List users, newest first: `{ users, total }`. */
  listUsers(options: AdminListUsersOptions = {}): Promise<AdminResult<AdminUserList>> {
    const params = new URLSearchParams()
    if (options.page !== undefined) params.set('page', String(options.page))
    if (options.perPage !== undefined) params.set('per_page', String(options.perPage))
    if (options.search) params.set('search', options.search)
    const qs = params.toString()
    return this.request('GET', qs ? '?' + qs : '')
  }

  /** Create a user. A taken email answers `{ status: 409, code: 'email_exists' }`. */
  createUser(params: AdminCreateUserParams): Promise<AdminResult<AdminUser>> {
    const body: Record<string, unknown> = { email: params.email }
    if (params.password !== undefined) body.password = params.password
    if (params.emailConfirmed !== undefined) body.email_confirmed = params.emailConfirmed
    if (params.displayName !== undefined) body.display_name = params.displayName
    if (params.metadata !== undefined) body.metadata = params.metadata
    return this.request('POST', '', body)
  }

  /** Get one user. Unknown id: `{ status: 404 }`. */
  getUserById(id: string): Promise<AdminResult<AdminUser>> {
    return this.request('GET', this.userPath(id))
  }

  /** Change a user's email, display name, metadata or confirmation. */
  updateUserById(id: string, params: AdminUpdateUserParams): Promise<AdminResult<AdminUser>> {
    const body: Record<string, unknown> = {}
    if (params.email !== undefined) body.email = params.email
    if (params.displayName !== undefined) body.display_name = params.displayName
    if (params.metadata !== undefined) body.metadata = params.metadata
    if (params.emailConfirmed !== undefined) body.email_confirmed = params.emailConfirmed
    return this.request('PATCH', this.userPath(id), body)
  }

  /**
   * Delete a user and everything that references them by foreign key with
   * ON DELETE CASCADE. Access tokens already issued stay valid until they
   * expire. `data` is null on success.
   */
  deleteUser(id: string): Promise<AdminResult<null>> {
    return this.request('DELETE', this.userPath(id))
  }

  /**
   * Suspend a user: they can't sign in, and every refresh token is revoked.
   * Access tokens already issued stay valid until they expire.
   */
  suspendUser(id: string): Promise<AdminResult<AdminUser>> {
    return this.request('POST', this.userPath(id, '/suspend'))
  }

  /** Lift a suspension. */
  unsuspendUser(id: string): Promise<AdminResult<AdminUser>> {
    return this.request('DELETE', this.userPath(id, '/suspend'))
  }

  /** Set a new password (8–72 bytes). Revokes every refresh token of the user. */
  setPassword(id: string, password: string): Promise<AdminResult<{ status: string }>> {
    return this.request('POST', this.userPath(id, '/password'), { password })
  }

  /**
   * Sign the user out everywhere: revokes every refresh token. Access
   * tokens already issued stay valid until they expire.
   */
  signOutUser(id: string): Promise<AdminResult<{ status: string; refresh_tokens_revoked: number }>> {
    return this.request('POST', this.userPath(id, '/signout'))
  }
}
