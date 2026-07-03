/**
 * Eurobase Auth Client — end-user authentication for SDK consumers.
 */

import type { HttpClient, EurobaseConfig } from './http'
import { httpClient } from './http'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

export interface AuthUser {
  id: string
  email: string
  display_name?: string
  avatar_url?: string
  metadata?: Record<string, any>
  created_at: string
  updated_at: string
}

export interface AuthSession {
  access_token: string
  token_type: string
  expires_in: number
  refresh_token: string
  user: AuthUser
}

export interface SignUpCredentials {
  email: string
  password: string
  metadata?: Record<string, any>
  /**
   * URL the verification email links to (query parameter `token` is
   * appended by the backend). Overrides
   * `auth_config.email_verification_url` for this signup only.
   *
   * MUST be a member of the project's `redirect_urls` allowlist or
   * the signup will 400 with a clear error. When omitted, the backend
   * falls back to the per-project default; when neither is set and
   * `require_email_confirmation` is on, the signup 400s with a
   * "configure email_verification_url" hint.
   *
   * See tenant docs, chapter 6 → "Email confirmation — end-to-end"
   * (#261).
   */
  emailRedirectTo?: string
}

/** Optional payload accepted by forgotPassword / requestMagicLink /
 * resendVerification for the per-request redirect override. */
export interface EmailRedirectOptions {
  /** See SignUpCredentials.emailRedirectTo — same contract. */
  emailRedirectTo?: string
}

export interface SignInCredentials {
  email: string
  password: string
}

export type AuthEvent = 'SIGNED_IN' | 'SIGNED_OUT' | 'TOKEN_REFRESHED'
export type AuthStateChangeCallback = (event: AuthEvent, session: AuthSession | null) => void

/** Status of an end-user-initiated DSAR export (Article 15/20). */
export type ExportStatus = 'pending' | 'running' | 'completed' | 'failed'

/** A row from POST /v1/auth/me/export or GET /v1/auth/me/export/{id}. */
export interface ExportRequest {
  id: string
  project_id: string
  /** Always the signed-in user's id for self-serve exports. */
  user_id?: string
  status: ExportStatus
  format: 'json' | 'csv'
  /** Only set when status === 'completed'. Presigned, 1h TTL. */
  download_url?: string
  /** Set when status === 'failed'. */
  error?: string
  file_size?: number
  /** When the download link itself stops working. 7 days after completion. */
  expires_at?: string
  started_at?: string
  completed_at?: string
  created_at: string
}

// ---------------------------------------------------------------------------
// AuthClient
// ---------------------------------------------------------------------------

export class AuthClient {
  private http: HttpClient
  private config: EurobaseConfig
  private session: AuthSession | null = null
  private listeners: AuthStateChangeCallback[] = []
  private refreshTimer: ReturnType<typeof setTimeout> | null = null
  private storageKey = 'eurobase_auth_session'

  constructor(config: EurobaseConfig, http: HttpClient) {
    this.http = http
    this.config = config
    this.restoreSession()
  }

  /**
   * Sign up a new end-user with email + password.
   *
   * If the project has `require_email_confirmation = true`, the user
   * is created with `email_confirmed_at = NULL` and a verification
   * email is sent to the tenant-configured URL (or the
   * `emailRedirectTo` override). Your app must have a route at that
   * URL that reads the `token` query param and calls
   * `eb.auth.verifyEmail(token)` to confirm the address.
   */
  async signUp(credentials: SignUpCredentials): Promise<{ data: AuthSession | null; error: string | null }> {
    // Map camelCase → snake_case for the wire format so the SDK
    // reads idiomatic JS but the backend keeps its existing schema.
    const body: Record<string, any> = {
      email: credentials.email,
      password: credentials.password,
    }
    if (credentials.metadata !== undefined) body.metadata = credentials.metadata
    if (credentials.emailRedirectTo !== undefined) body.email_redirect_to = credentials.emailRedirectTo
    const result = await this.http.post('/v1/auth/signup', body)
    if (result.error) {
      return { data: null, error: result.error }
    }
    this.setSession(result)
    this.emit('SIGNED_IN', result)
    return { data: result, error: null }
  }

  /** Sign in an existing end-user with email + password. */
  async signIn(credentials: SignInCredentials): Promise<{ data: AuthSession | null; error: string | null }> {
    const result = await this.http.post('/v1/auth/signin', credentials)
    if (result.error) {
      return { data: null, error: result.error }
    }
    this.setSession(result)
    this.emit('SIGNED_IN', result)
    return { data: result, error: null }
  }

  /** Sign out the current user. */
  async signOut(): Promise<{ error: string | null }> {
    if (this.session) {
      await this.http.post('/v1/auth/signout', {
        refresh_token: this.session.refresh_token,
      })
    }
    this.clearSession()
    this.emit('SIGNED_OUT', null)
    return { error: null }
  }

  /** Refresh the current session using the refresh token. */
  async refreshSession(): Promise<{ data: AuthSession | null; error: string | null }> {
    if (!this.session?.refresh_token) {
      return { data: null, error: 'no refresh token' }
    }
    const result = await this.http.post('/v1/auth/refresh', {
      refresh_token: this.session.refresh_token,
    })
    if (result.error) {
      this.clearSession()
      this.emit('SIGNED_OUT', null)
      return { data: null, error: result.error }
    }
    this.setSession(result)
    this.emit('TOKEN_REFRESHED', result)
    return { data: result, error: null }
  }

  /**
   * Request a magic link email for passwordless sign-in.
   *
   * The email links to the tenant-configured `magic_link_url` (or
   * the `emailRedirectTo` override) with a `token` query parameter
   * appended. Your app should read that token and call
   * `signInWithMagicLink(token)` to complete sign-in.
   *
   * Always returns `error: null` — the endpoint intentionally never
   * enumerates. Bad configuration surfaces in your log drain
   * (`per_request_redirect_rejected` if you passed a URL not in
   * `redirect_urls`), not as an API response.
   */
  async requestMagicLink(email: string, options?: EmailRedirectOptions): Promise<{ error: string | null }> {
    const body: Record<string, any> = { email }
    if (options?.emailRedirectTo !== undefined) body.email_redirect_to = options.emailRedirectTo
    const result = await this.http.post('/v1/auth/request-magic-link', body)
    if (result.error) {
      return { error: result.error }
    }
    return { error: null }
  }

  /** Sign in with a magic link token. */
  async signInWithMagicLink(token: string): Promise<{ data: AuthSession | null; error: string | null }> {
    const result = await this.http.post('/v1/auth/signin-magic-link', { token })
    if (result.error) {
      return { data: null, error: result.error }
    }
    this.setSession(result)
    this.emit('SIGNED_IN', result)
    return { data: result, error: null }
  }

  /**
   * Confirm an end-user's email address using a token from a
   * verification email. Call this from the page you configured as
   * `email_verification_url` (or `emailRedirectTo`), after reading
   * the `token` query parameter.
   *
   * @example
   * ```ts
   * const params = new URL(location.href).searchParams
   * const token = params.get('token')
   * if (token) {
   *   const { error } = await eb.auth.verifyEmail(token)
   * }
   * ```
   */
  async verifyEmail(token: string): Promise<{ error: string | null }> {
    const result = await this.http.post('/v1/auth/verify-email', { token })
    if (result.error) {
      return { error: result.error }
    }
    return { error: null }
  }

  /**
   * Trigger a password-reset email. Same redirect-URL contract as
   * `signUp`: sends the user to the tenant-configured
   * `password_reset_url` (or `emailRedirectTo` override) with a
   * `token` query parameter.
   *
   * Always returns `error: null` to prevent email enumeration —
   * whether the address is registered or not, the API responds
   * identically.
   */
  async forgotPassword(email: string, options?: EmailRedirectOptions): Promise<{ error: string | null }> {
    const body: Record<string, any> = { email }
    if (options?.emailRedirectTo !== undefined) body.email_redirect_to = options.emailRedirectTo
    const result = await this.http.post('/v1/auth/forgot-password', body)
    if (result.error) {
      return { error: result.error }
    }
    return { error: null }
  }

  /**
   * Complete a password reset using a token from a reset email.
   * Call this from the page you configured as `password_reset_url`
   * (or `emailRedirectTo`), after reading the `token` query
   * parameter and collecting the user's new password.
   *
   * @example
   * ```ts
   * const params = new URL(location.href).searchParams
   * const token = params.get('token')
   * if (token) {
   *   const { error } = await eb.auth.resetPassword(token, newPassword)
   * }
   * ```
   */
  async resetPassword(token: string, newPassword: string): Promise<{ error: string | null }> {
    // Backend handler at /v1/auth/reset-password expects the field
    // literally named `password` (not `new_password`). The SDK
    // parameter stays `newPassword` for JS ergonomics — reserved
    // words are fine as identifiers but `password` alone reads as
    // "the user's current password", which resetPassword is not.
    const result = await this.http.post('/v1/auth/reset-password', {
      token,
      password: newPassword,
    })
    if (result.error) {
      return { error: result.error }
    }
    return { error: null }
  }

  /**
   * Resend the verification email to an unconfirmed user. Rate
   * limited per email; already-confirmed users silently no-op. Same
   * redirect-URL contract as `signUp`.
   *
   * Always returns `error: null` — the endpoint intentionally never
   * enumerates.
   */
  async resendVerification(email: string, options?: EmailRedirectOptions): Promise<{ error: string | null }> {
    const body: Record<string, any> = { email }
    if (options?.emailRedirectTo !== undefined) body.email_redirect_to = options.emailRedirectTo
    const result = await this.http.post('/v1/auth/resend-verification', body)
    if (result.error) {
      return { error: result.error }
    }
    return { error: null }
  }

  /**
   * Initiate OAuth sign-in by redirecting the browser to the provider's consent screen.
   * After the user authorizes, they will be redirected back to `redirectTo` with tokens
   * in the URL hash fragment.
   */
  signInWithOAuth(provider: string, options?: { redirectTo?: string }): void {
    const baseUrl = this.config.url.replace(/\/+$/, '')
    const redirectTo = options?.redirectTo ?? (typeof window !== 'undefined' ? window.location.origin : '')
    const url = `${baseUrl}/v1/auth/oauth/${encodeURIComponent(provider)}?redirect_url=${encodeURIComponent(redirectTo)}`
    if (typeof window !== 'undefined') {
      window.location.href = url
    }
  }

  /**
   * Handle the OAuth callback by reading tokens from the URL hash fragment.
   * Call this on your callback page after the OAuth redirect.
   * Returns the session if tokens are found, or null.
   */
  handleOAuthCallback(): { data: AuthSession | null; error: string | null } {
    if (typeof window === 'undefined') {
      return { data: null, error: 'not in browser environment' }
    }

    // Check for error in query params.
    const searchParams = new URLSearchParams(window.location.search)
    const errorParam = searchParams.get('error')
    if (errorParam) {
      const errorDesc = searchParams.get('error_description') || errorParam
      return { data: null, error: errorDesc }
    }

    // Read tokens from URL hash fragment.
    const hash = window.location.hash.substring(1)
    if (!hash) {
      return { data: null, error: null }
    }

    const params = new URLSearchParams(hash)
    const accessToken = params.get('access_token')
    const refreshToken = params.get('refresh_token')
    const tokenType = params.get('token_type') || 'bearer'
    const expiresIn = parseInt(params.get('expires_in') || '3600', 10)

    if (!accessToken || !refreshToken) {
      return { data: null, error: null }
    }

    const session: AuthSession = {
      access_token: accessToken,
      token_type: tokenType,
      expires_in: expiresIn,
      refresh_token: refreshToken,
      user: { id: '', email: '', created_at: '', updated_at: '' },
    }

    this.setSession(session)
    this.emit('SIGNED_IN', session)

    // Clean the URL hash.
    if (typeof window.history !== 'undefined') {
      window.history.replaceState(null, '', window.location.pathname + window.location.search)
    }

    return { data: session, error: null }
  }

  /** Get the current user from the server. */
  async getUser(): Promise<{ data: AuthUser | null; error: string | null }> {
    const result = await this.http.get('/v1/auth/user')
    if (result.error) {
      return { data: null, error: result.error }
    }
    return { data: result, error: null }
  }

  /**
   * Request a DSAR export of the signed-in user's own data
   * (GDPR Article 15 / 20). Returns the queued export request — poll
   * with `getMyExport(id)` until `status === 'completed'`, then read
   * `download_url`.
   *
   * Rate-limited per user (1 export / 24h on Free; configurable on
   * higher tiers). The download link, once issued, expires after 7
   * days.
   *
   * @example
   * const { data } = await eb.auth.exportMyData('json')
   * if (data) {
   *   // poll
   *   const { data: ready } = await eb.auth.getMyExport(data.id)
   *   if (ready?.status === 'completed') window.location = ready.download_url!
   * }
   */
  async exportMyData(
    format: 'json' | 'csv' = 'json',
  ): Promise<{ data: ExportRequest | null; error: string | null }> {
    const result = await this.http.post('/v1/auth/me/export', { format })
    if (result?.error) {
      return { data: null, error: result.error }
    }
    return { data: result as ExportRequest, error: null }
  }

  /**
   * Fetch the status of an export the signed-in user previously
   * requested. Returns `status` + (when completed) a presigned
   * `download_url`.
   *
   * Each call generates a fresh 1-hour-TTL download URL — safe to
   * call repeatedly.
   */
  async getMyExport(
    exportId: string,
  ): Promise<{ data: ExportRequest | null; error: string | null }> {
    const result = await this.http.get(`/v1/auth/me/export/${encodeURIComponent(exportId)}`)
    if (result?.error) {
      return { data: null, error: result.error }
    }
    return { data: result as ExportRequest, error: null }
  }

  /** Get the current session (from memory, not server). */
  getSession(): AuthSession | null {
    return this.session
  }

  /** Subscribe to auth state changes. Returns an unsubscribe function. */
  onAuthStateChange(callback: AuthStateChangeCallback): () => void {
    this.listeners.push(callback)
    // Fire immediately with current state.
    if (this.session) {
      callback('SIGNED_IN', this.session)
    }
    return () => {
      this.listeners = this.listeners.filter((l) => l !== callback)
    }
  }

  // ── Internal ──

  private setSession(session: AuthSession) {
    this.session = session
    this.http.setAccessToken(session.access_token)
    this.persistSession(session)
    this.scheduleRefresh(session.expires_in)
  }

  private clearSession() {
    this.session = null
    this.http.setAccessToken(null)
    this.removePersistedSession()
    if (this.refreshTimer) {
      clearTimeout(this.refreshTimer)
      this.refreshTimer = null
    }
  }

  private scheduleRefresh(expiresIn: number) {
    if (this.refreshTimer) {
      clearTimeout(this.refreshTimer)
    }
    // Refresh 60 seconds before expiry.
    const refreshIn = Math.max((expiresIn - 60) * 1000, 5000)
    this.refreshTimer = setTimeout(() => {
      this.refreshSession()
    }, refreshIn)
  }

  private emit(event: AuthEvent, session: AuthSession | null) {
    for (const listener of this.listeners) {
      try {
        listener(event, session)
      } catch {
        // Don't let listener errors crash the auth flow.
      }
    }
  }

  private persistSession(session: AuthSession) {
    try {
      if (typeof localStorage !== 'undefined') {
        localStorage.setItem(this.storageKey, JSON.stringify(session))
      }
    } catch {
      // localStorage not available (Node.js, etc.)
    }
  }

  private removePersistedSession() {
    try {
      if (typeof localStorage !== 'undefined') {
        localStorage.removeItem(this.storageKey)
      }
    } catch {
      // localStorage not available
    }
  }

  private restoreSession() {
    try {
      if (typeof localStorage !== 'undefined') {
        const raw = localStorage.getItem(this.storageKey)
        if (raw) {
          const session = JSON.parse(raw) as AuthSession
          this.setSession(session)
        }
      }
    } catch {
      // Ignore parse errors
    }
  }
}
