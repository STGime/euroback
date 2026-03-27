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
}

export interface SignInCredentials {
  email: string
  password: string
}

export type AuthEvent = 'SIGNED_IN' | 'SIGNED_OUT' | 'TOKEN_REFRESHED'
export type AuthStateChangeCallback = (event: AuthEvent, session: AuthSession | null) => void

// ---------------------------------------------------------------------------
// AuthClient
// ---------------------------------------------------------------------------

export class AuthClient {
  private http: HttpClient
  private session: AuthSession | null = null
  private listeners: AuthStateChangeCallback[] = []
  private refreshTimer: ReturnType<typeof setTimeout> | null = null
  private storageKey = 'eurobase_auth_session'

  constructor(config: EurobaseConfig, http: HttpClient) {
    this.http = http
    this.restoreSession()
  }

  /** Sign up a new end-user with email + password. */
  async signUp(credentials: SignUpCredentials): Promise<{ data: AuthSession | null; error: string | null }> {
    const result = await this.http.post('/v1/auth/signup', credentials)
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

  /** Request a magic link email for passwordless sign-in. */
  async requestMagicLink(email: string): Promise<{ error: string | null }> {
    const result = await this.http.post('/v1/auth/request-magic-link', { email })
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

  /** Get the current user from the server. */
  async getUser(): Promise<{ data: AuthUser | null; error: string | null }> {
    const result = await this.http.get('/v1/auth/user')
    if (result.error) {
      return { data: null, error: result.error }
    }
    return { data: result, error: null }
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
