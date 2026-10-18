/**
 * Eurobase Auth Client — end-user authentication for SDK consumers.
 */

import type { HttpClient, EurobaseConfig } from './http'
import { httpClient } from './http'
import { passkeysSupported, createPasskey, getPasskeyAssertion, passkeyErrorMessage } from './webauthn'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/** setTimeout's largest delay (2^31 - 1 ms, about 24.8 days). */
const MAX_TIMEOUT_MS = 2 ** 31 - 1

/** First retry after a failed refresh; doubles up to REFRESH_RETRY_MAX_MS. */
const REFRESH_RETRY_MIN_MS = 5_000
const REFRESH_RETRY_MAX_MS = 60_000

/** How long a tab waits for another tab's refresh before refreshing itself. */
const REFRESH_LOCK_WAIT_MS = 10_000

/**
 * Statuses from /v1/auth/refresh that end the session: the token is
 * unknown, revoked or expired (401), the request is malformed (400), or the
 * user must confirm their email first (403). Anything else — no response,
 * 429, 5xx, maintenance — is temporary: keep the session and retry (#814).
 */
function endsSession(status: unknown): boolean {
  return status === 400 || status === 401 || status === 403
}

/** True for a server response (or stored value) that is a usable session. */
function isSession(value: unknown): value is AuthSession {
  const v = value as Partial<AuthSession> | null
  return !!v && typeof v === 'object'
    && typeof v.access_token === 'string' && v.access_token !== ''
    && typeof v.refresh_token === 'string' && v.refresh_token !== ''
}

/** Error returned when a sign-in response isn't a session. */
const INVALID_SESSION_RESPONSE = 'invalid session in server response'

function setTimer(fn: () => void, ms: number): ReturnType<typeof setTimeout> {
  const t = setTimeout(fn, ms)
  // In Node a pending refresh must not keep the process alive — a script
  // that signs in would otherwise hang until the token nears expiry
  // (~1 h). Browsers return a number here, so this is a no-op.
  ;(t as { unref?: () => void }).unref?.()
  return t
}

/**
 * Read the claims of a JWT without verifying it (the gateway verifies every
 * request; this only fills the client-side session). Returns null for
 * anything that isn't a decodable three-part token.
 */
function decodeJwtClaims(token: unknown): Record<string, unknown> | null {
  if (typeof token !== 'string') return null
  const parts = token.split('.')
  if (parts.length !== 3 || typeof atob !== 'function') return null
  try {
    let b64 = parts[1].replace(/-/g, '+').replace(/_/g, '/')
    b64 += '='.repeat((4 - (b64.length % 4)) % 4)
    const bin = atob(b64)
    const bytes = new Uint8Array(bin.length)
    for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i)
    const json = typeof TextDecoder !== 'undefined' ? new TextDecoder().decode(bytes) : bin
    const claims = JSON.parse(json)
    return claims && typeof claims === 'object' ? claims : null
  } catch {
    return null
  }
}

export interface AuthUser {
  id: string
  email: string
  display_name?: string
  avatar_url?: string
  metadata?: Record<string, any>
  /**
   * When the user confirmed their email (ISO timestamp), or null while
   * unconfirmed. Requires gateway #806; absent from older gateways.
   */
  email_confirmed_at?: string | null
  created_at: string
  updated_at: string
}

/**
 * Error string returned by `signIn`, `signInWithPasskey` and
 * `refreshSession` when the project requires email confirmation and the
 * user hasn't confirmed yet (`signIn` only after the correct password).
 * Offer `resendVerification(email)`.
 */
export const EMAIL_NOT_CONFIRMED = 'email_not_confirmed'

/** Result of `signUp`. */
export interface SignUpResult {
  /** The session — null when the user must confirm their email first. */
  data: AuthSession | null
  error: string | null
  /** The new user, also when there's no session yet. */
  user?: AuthUser
  /**
   * True when the project requires email confirmation: a verification
   * email was sent, and the user can sign in after confirming.
   */
  confirmationRequired?: boolean
}

export interface AuthSession {
  access_token: string
  token_type: string
  expires_in: number
  /**
   * When the access token expires, in Unix seconds (client clock). Set by
   * the SDK when it receives the session, so a session restored from
   * storage refreshes on time.
   */
  expires_at?: number
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
  /** Set when status === 'failed' — a generic message; the app's developer
   *  sees the details in the console. */
  error?: string
  /** Set once completed: false when the archive is missing something (a
   *  table that couldn't be read, a truncated table, …). The app's
   *  developer sees the details in the console. */
  complete?: boolean
  file_size?: number
  /** When the download link itself stops working. 7 days after completion. */
  expires_at?: string
  started_at?: string
  completed_at?: string
  created_at: string
}

/** A passkey (WebAuthn credential) enrolled on the signed-in user's
 *  account. Returned by `registerPasskey()` and `listPasskeys()`. */
export interface Passkey {
  id: string
  /** User-chosen label, or null if none was given at enrolment. */
  nickname: string | null
  created_at: string
  /** Last time this passkey was used to sign in; null if never. */
  last_used_at: string | null
  /** The authenticator reports the credential as synced across the user's
   *  devices (iCloud Keychain, Google Password Manager, 1Password, …),
   *  vs. bound to a single device. */
  backed_up: boolean
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
  /**
   * Bumped on every session change (set, clear, adopted from another tab).
   * A refresh whose response arrives after a change drops its result, so a
   * sign-out during a refresh stays signed out (#814).
   */
  private generation = 0
  /** The refresh in flight, shared by concurrent callers. */
  private refreshing: Promise<{ data: AuthSession | null; error: string | null }> | null = null
  /** Failed refresh attempts in a row (temporary errors only). */
  private refreshAttempts = 0

  constructor(config: EurobaseConfig, http: HttpClient) {
    this.http = http
    this.config = config
    this.restoreSession()
    this.listenToOtherTabs()
  }

  /**
   * Sign up a new end-user with email + password.
   *
   * If the project has `require_email_confirmation = true`, the user
   * is created with `email_confirmed_at = NULL` and a verification
   * email is sent to the tenant-configured URL (or the
   * `emailRedirectTo` override). Your app must have a route at that
   * URL that reads the `token` query param and calls
   * `eb.auth.verifyEmail(token)` to confirm the address. In that case
   * there is no session yet: `data` is null, `confirmationRequired` is
   * true and `user` is the new user (gateway #806).
   */
  async signUp(credentials: SignUpCredentials): Promise<SignUpResult> {
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
    if (result.confirmation_required || !result.access_token) {
      return { data: null, error: null, user: result.user, confirmationRequired: true }
    }
    const session = this.setSession(result)
    this.emit('SIGNED_IN', session)
    return { data: session, error: null, user: session.user, confirmationRequired: false }
  }

  /** Sign in an existing end-user with email + password. */
  async signIn(credentials: SignInCredentials): Promise<{ data: AuthSession | null; error: string | null }> {
    const result = await this.http.post('/v1/auth/signin', credentials)
    if (result.error) {
      return { data: null, error: result.error }
    }
    if (!isSession(result)) {
      return { data: null, error: INVALID_SESSION_RESPONSE }
    }
    const session = this.setSession(result)
    this.emit('SIGNED_IN', session)
    return { data: session, error: null }
  }

  /**
   * Sign out the current user. The session is cleared at once (also in
   * the app's other tabs); then the refresh token is revoked on the server.
   */
  async signOut(): Promise<{ error: string | null }> {
    const refreshToken = this.session?.refresh_token
    this.clearSession()
    this.emit('SIGNED_OUT', null)
    if (refreshToken) {
      await this.http.post('/v1/auth/signout', { refresh_token: refreshToken })
    }
    return { error: null }
  }

  /**
   * Refresh the current session using the refresh token.
   *
   * The session ends (`SIGNED_OUT`) only when the server rejects the
   * refresh token (400 / 401 / 403, e.g. revoked, expired, or
   * `email_not_confirmed`). On a network error, a rate limit or a server
   * error the session is kept, the error is returned, and the SDK retries
   * on its own with backoff — the first retries before the access token
   * expires (#814).
   *
   * Calls made while a refresh is running share it. In a browser, tabs
   * of the same app share the session: only one refreshes at a time, and
   * the others take the session it stores.
   */
  async refreshSession(): Promise<{ data: AuthSession | null; error: string | null }> {
    if (!this.refreshing) {
      this.refreshing = this.runRefresh().finally(() => {
        this.refreshing = null
      })
    }
    return this.refreshing
  }

  private async runRefresh(): Promise<{ data: AuthSession | null; error: string | null }> {
    if (!this.session?.refresh_token) {
      return { data: null, error: 'no refresh token' }
    }
    return this.withRefreshLock(async () => {
      // Another tab may have refreshed while this one waited for the lock
      // (or missed its storage event): take its session instead of using a
      // refresh token that the server has already rotated.
      if (this.adoptStoredSession()) {
        this.refreshAttempts = 0
        return { data: this.session, error: null }
      }
      if (!this.session?.refresh_token) {
        return { data: null, error: 'no refresh token' }
      }
      const generation = this.generation
      const result = await this.http.post('/v1/auth/refresh', {
        refresh_token: this.session.refresh_token,
      })

      if (generation !== this.generation) {
        // The session changed during the request (sign-out, sign-in,
        // another tab). Keep that change; drop this result.
        if (!this.session && isSession(result)) {
          // Signed out: revoke the token the server just issued.
          void this.http.post('/v1/auth/signout', { refresh_token: result.refresh_token })
        }
        return this.session
          ? { data: this.session, error: null }
          : { data: null, error: 'signed out' }
      }

      if (result.error && endsSession(result.status)) {
        // Rejected — unless another tab rotated the token meanwhile.
        if (this.adoptStoredSession()) {
          this.refreshAttempts = 0
          return { data: this.session, error: null }
        }
        this.refreshAttempts = 0
        this.clearSession()
        this.emit('SIGNED_OUT', null)
        return { data: null, error: result.error }
      }

      if (result.error || !isSession(result)) {
        this.scheduleRetry()
        return { data: null, error: result.error || INVALID_SESSION_RESPONSE }
      }

      this.refreshAttempts = 0
      const session = this.setSession(result)
      this.emit('TOKEN_REFRESHED', session)
      return { data: session, error: null }
    })
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
    if (!isSession(result)) {
      return { data: null, error: INVALID_SESSION_RESPONSE }
    }
    const session = this.setSession(result)
    this.emit('SIGNED_IN', session)
    return { data: session, error: null }
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
   * Change the signed-in user's password (#808). Needs the current
   * password. All of the user's refresh tokens are revoked, so other
   * sessions end once their current access token expires; this client gets
   * a fresh session and emits `TOKEN_REFRESHED`. Users without a password (magic link, OAuth) set
   * one with `forgotPassword()` / `resetPassword()` instead.
   * Requires gateway #808.
   */
  async updatePassword(params: { currentPassword: string; newPassword: string }): Promise<{ data: AuthSession | null; error: string | null }> {
    if (!this.session) {
      return { data: null, error: 'not signed in' }
    }
    const result = await this.http.post('/v1/auth/user/password', {
      current_password: params.currentPassword,
      new_password: params.newPassword,
    })
    if (result.error) {
      return { data: null, error: result.error }
    }
    if (!isSession(result)) {
      return { data: null, error: INVALID_SESSION_RESPONSE }
    }
    const session = this.setSession(result)
    this.emit('TOKEN_REFRESHED', session)
    return { data: session, error: null }
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

  // ── Passkeys (#630) ──

  /**
   * True when this browser can run a passkey ceremony at all (WebAuthn +
   * `navigator.credentials` present). Always false in Node / SSR. Use it
   * to decide whether to show a "Sign in with a passkey" button.
   */
  passkeysSupported(): boolean {
    return passkeysSupported()
  }

  /**
   * Enrol a passkey on the **currently signed-in** user's account
   * (register ceremony). Requires an active session — a passkey can only
   * be added by the account's own holder, so a stolen long-lived session
   * can't silently enrol an attacker credential.
   *
   * Runs the whole two-step ceremony: fetches options from the gateway,
   * calls `navigator.credentials.create()` (the browser prompts for Face
   * ID / Touch ID / security key), then posts the attestation back.
   *
   * Passkeys must be enabled for the project (console → Auth → Passkeys)
   * or this returns an error. Browser cancellation / timeout surfaces as
   * a friendly `error` string, not a throw.
   *
   * @param nickname Optional label shown in the user's passkey list.
   * @example
   * if (eb.auth.passkeysSupported()) {
   *   const { data, error } = await eb.auth.registerPasskey('MacBook')
   * }
   */
  async registerPasskey(nickname?: string): Promise<{ data: Passkey | null; error: string | null }> {
    if (!passkeysSupported()) {
      return { data: null, error: 'passkeys are not supported in this environment' }
    }
    const begin = await this.http.post('/v1/auth/passkey/register/begin', {})
    if (begin?.error) {
      return { data: null, error: begin.error }
    }
    let credential: Record<string, unknown>
    try {
      credential = await createPasskey(begin.options)
    } catch (err) {
      return { data: null, error: passkeyErrorMessage(err) }
    }
    const finish = await this.http.post('/v1/auth/passkey/register/finish', {
      challenge_id: begin.challenge_id,
      credential,
      nickname: nickname ?? '',
    })
    if (finish?.error) {
      return { data: null, error: finish.error }
    }
    return { data: finish as Passkey, error: null }
  }

  /**
   * Sign in with a passkey — passwordless, no email needed (discoverable
   * credential / resident key). The browser lists the passkeys registered
   * for this site and the user picks one; on success a session is
   * established exactly like `signIn()` (sets the session, emits
   * `SIGNED_IN`).
   *
   * Passkeys must be enabled for the project. Browser cancellation /
   * timeout surfaces as a friendly `error` string, not a throw.
   *
   * @example
   * const { data, error } = await eb.auth.signInWithPasskey()
   * if (data) { // signed in }
   */
  async signInWithPasskey(): Promise<{ data: AuthSession | null; error: string | null }> {
    if (!passkeysSupported()) {
      return { data: null, error: 'passkeys are not supported in this environment' }
    }
    const begin = await this.http.post('/v1/auth/passkey/login/begin', {})
    if (begin?.error) {
      return { data: null, error: begin.error }
    }
    let credential: Record<string, unknown>
    try {
      credential = await getPasskeyAssertion(begin.options)
    } catch (err) {
      return { data: null, error: passkeyErrorMessage(err) }
    }
    const result = await this.http.post('/v1/auth/passkey/login/finish', {
      challenge_id: begin.challenge_id,
      credential,
    })
    if (result?.error) {
      return { data: null, error: result.error }
    }
    if (!isSession(result)) {
      return { data: null, error: INVALID_SESSION_RESPONSE }
    }
    const session = this.setSession(result)
    this.emit('SIGNED_IN', session)
    return { data: session, error: null }
  }

  /**
   * List the passkeys enrolled on the signed-in user's account. Requires
   * an active session.
   */
  async listPasskeys(): Promise<{ data: Passkey[] | null; error: string | null }> {
    const result = await this.http.get('/v1/auth/passkey')
    if (result?.error) {
      return { data: null, error: result.error }
    }
    return { data: (result?.passkeys ?? []) as Passkey[], error: null }
  }

  /**
   * Delete one of the signed-in user's passkeys by id (from
   * `listPasskeys()`). Requires an active session. Deleting the last
   * passkey is allowed — make sure the user still has another way to sign
   * in (password / magic link) before relying on this.
   */
  async deletePasskey(id: string): Promise<{ error: string | null }> {
    const result = await this.http.del(`/v1/auth/passkey/${encodeURIComponent(id)}`)
    if (result?.error) {
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
    this.emit('SIGNED_IN', this.session!)

    // Clean the URL hash.
    if (typeof window.history !== 'undefined') {
      window.history.replaceState(null, '', window.location.pathname + window.location.search)
    }

    return { data: this.session, error: null }
  }

  /**
   * Get the current user from the server. Also replaces the stored
   * session's `user` with it (same user id only — the server's profile is
   * authoritative, so cleared fields don't linger), so `getSession().user`
   * carries the full profile afterwards.
   */
  async getUser(): Promise<{ data: AuthUser | null; error: string | null }> {
    const result = await this.http.get('/v1/auth/user')
    if (result.error) {
      return { data: null, error: result.error }
    }
    if (this.session && result?.id && result.id === this.session.user?.id) {
      this.session = { ...this.session, user: result as AuthUser }
      this.persistSession(this.session)
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

  private setSession(session: AuthSession, restored = false): AuthSession {
    // An OAuth callback (and a session stored by SDK ≤ 0.8.0 after one)
    // has no user yet: take id / email from the access token's claims
    // (`sub`, `email`) so getSession().user.id is usable right away (#720).
    // Unverified until the first server call — the gateway verifies every
    // request; never base client-side authorization on it.
    if (!session.user?.id) {
      const claims = decodeJwtClaims(session.access_token)
      if (claims && typeof claims.sub === 'string' && claims.sub) {
        session = {
          ...session,
          user: {
            ...session.user,
            created_at: session.user?.created_at ?? '',
            updated_at: session.user?.updated_at ?? '',
            id: claims.sub,
            // Phone sign-ins put the phone number in `email`; only take an address.
            email: typeof claims.email === 'string' && claims.email.includes('@')
              ? claims.email
              : (session.user?.email ?? ''),
          },
        }
      }
    }
    // A session from the server counts expires_in from now; a restored
    // one keeps its stored expiry (older SDKs didn't store it: use the
    // token's exp). Counting from now keeps a fresh session independent
    // of the client's clock.
    let expiresAt = restored ? session.expires_at : undefined
    if (restored && typeof expiresAt !== 'number') {
      const exp = decodeJwtClaims(session.access_token)?.exp
      if (typeof exp === 'number') expiresAt = exp
    }
    if (typeof expiresAt !== 'number') {
      expiresAt = Math.floor(Date.now() / 1000) + (Number(session.expires_in) || 3600)
    }
    session = { ...session, expires_at: expiresAt }
    this.generation++
    this.session = session
    this.http.setAccessToken(session.access_token)
    this.persistSession(session)
    this.scheduleRefresh(expiresAt)
    return session
  }

  private clearSession() {
    this.generation++
    this.refreshAttempts = 0
    this.session = null
    this.http.setAccessToken(null)
    this.removePersistedSession()
    if (this.refreshTimer) {
      clearTimeout(this.refreshTimer)
      this.refreshTimer = null
    }
  }

  private scheduleRefresh(expiresAt: number) {
    if (this.refreshTimer) {
      clearTimeout(this.refreshTimer)
      this.refreshTimer = null
    }
    // Refresh 60 seconds before expiry (at least 5 s from now). In a
    // browser without Web Locks, up to 10 s of jitter keeps tabs that
    // restored the same session from refreshing at the same moment.
    const jitter = typeof window !== 'undefined' && !webLocks() ? Math.random() * 10_000 : 0
    const delay = Math.max(expiresAt * 1000 - 60_000 - jitter - Date.now(), 5000)
    // setTimeout stores the delay as a 32-bit int: anything longer
    // (sessions over ~24.8 days) would fire at once and refresh in a
    // loop (#804). Wait the maximum and check again instead.
    const capped = delay > MAX_TIMEOUT_MS
    this.refreshTimer = setTimer(() => {
      this.refreshTimer = null
      if (capped) this.scheduleRefresh(expiresAt)
      else void this.refreshSession()
    }, capped ? MAX_TIMEOUT_MS : delay)
  }

  /**
   * After a temporary refresh failure: try again with backoff (5 s,
   * 10 s, … up to 60 s). While the access token is still valid, the next
   * try comes before it expires.
   */
  private scheduleRetry() {
    if (!this.session) return
    if (this.refreshTimer) {
      clearTimeout(this.refreshTimer)
      this.refreshTimer = null
    }
    this.refreshAttempts++
    let delay = Math.min(REFRESH_RETRY_MIN_MS * 2 ** (this.refreshAttempts - 1), REFRESH_RETRY_MAX_MS)
    const untilExpiry = (this.session.expires_at ?? 0) * 1000 - Date.now()
    if (untilExpiry > 0) {
      delay = Math.max(Math.min(delay, untilExpiry - 1000), 1000)
    }
    this.refreshTimer = setTimer(() => {
      this.refreshTimer = null
      void this.refreshSession()
    }, delay)
  }

  /**
   * Run a refresh while holding a lock shared by the app's tabs (Web
   * Locks), so only one tab uses the refresh token at a time — the server
   * rotates it, and a second tab using the old one would be rejected.
   * Without Web Locks (Node, older browsers), or if the lock isn't granted
   * within REFRESH_LOCK_WAIT_MS, the refresh runs without it.
   */
  private async withRefreshLock<T>(fn: () => Promise<T>): Promise<T> {
    const locks = webLocks()
    if (!locks) return fn()
    let started = false
    const run = () => {
      started = true
      return fn()
    }
    try {
      const signal = typeof AbortSignal !== 'undefined' && typeof AbortSignal.timeout === 'function'
        ? AbortSignal.timeout(REFRESH_LOCK_WAIT_MS)
        : undefined
      return await locks.request(`${this.storageKey}:refresh`, signal ? { signal } : {}, run)
    } catch (err) {
      if (started) throw err
      return fn()
    }
  }

  /**
   * Keep this tab's session in step with the app's other tabs: a session
   * another tab stored (refreshed, or a new sign-in) is taken over, and a
   * removed one signs this tab out too.
   */
  private listenToOtherTabs() {
    if (typeof window === 'undefined' || typeof window.addEventListener !== 'function') return
    window.addEventListener('storage', (event: StorageEvent) => {
      // key null = the other tab called localStorage.clear().
      if (event.key !== null && event.key !== this.storageKey) return
      if (this.adoptStoredSession()) return
      if (this.session && !this.storedValuePresent()) {
        this.clearSession()
        this.emit('SIGNED_OUT', null)
      }
    })
    // Back online after temporary refresh failures: retry now.
    window.addEventListener('online', () => {
      if (this.session && this.refreshAttempts > 0) void this.refreshSession()
    })
  }

  /**
   * If storage holds a different valid session than this tab (another tab
   * refreshed or signed in), take it over and return true.
   */
  private adoptStoredSession(): boolean {
    const stored = this.readStoredSession()
    if (!stored) return false
    const previous = this.session
    if (previous && stored.refresh_token === previous.refresh_token) return false
    const session = this.setSession(stored, true)
    const sameUser = !!previous && previous.user?.id === session.user?.id
    this.emit(sameUser ? 'TOKEN_REFRESHED' : 'SIGNED_IN', session)
    return true
  }

  private readStoredSession(): AuthSession | null {
    try {
      if (typeof localStorage === 'undefined') return null
      const raw = localStorage.getItem(this.storageKey)
      if (!raw) return null
      const parsed = JSON.parse(raw)
      return isSession(parsed) ? parsed : null
    } catch {
      return null
    }
  }

  private storedValuePresent(): boolean {
    try {
      return typeof localStorage !== 'undefined' && localStorage.getItem(this.storageKey) !== null
    } catch {
      return false
    }
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
    const session = this.readStoredSession()
    if (session) this.setSession(session, true)
  }
}

/** The Web Locks API, when the runtime has it (modern browsers). */
function webLocks(): { request: (name: string, options: object, fn: () => Promise<any>) => Promise<any> } | null {
  const locks = typeof navigator !== 'undefined' ? (navigator as any).locks : undefined
  return locks && typeof locks.request === 'function' ? locks : null
}
