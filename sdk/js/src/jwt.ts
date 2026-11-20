/**
 * Verification of end-user access tokens on a server (#809 keys).
 *
 * The gateway signs end-user access tokens with ES256, using a P-256 key
 * pair that belongs to the project, and publishes the public key at
 * `GET /v1/auth/.well-known/jwks.json` on the project's own URL. This
 * module verifies a token locally against that key with WebCrypto (no
 * dependencies). When the environment prevents a local check — no
 * WebCrypto in the runtime, JWKS unreachable — the gateway checks the token
 * instead (`GET /v1/auth/user` with the token), unless the caller turns that
 * off. HS256 tokens are refused unless the caller opts in (`allowHS256`):
 * anyone can mint a token that claims HS256, and sending each one to the
 * gateway would let them spend the server's rate limit.
 */

import type { EurobaseConfig, HttpClient } from './http'

/** What `auth.verifyAccessToken()` returns for a valid token. */
export interface VerifiedAccessToken {
  /** The user's id (`sub`). */
  userId: string
  /** The user's email (the phone number for phone sign-in), or '' if none. */
  email: string
  /** Always `'authenticated'`: the RLS role the gateway gives this token. */
  role: 'authenticated'
  /** When the token expires, in Unix seconds. */
  expiresAt: number
  /** All claims of the token. */
  claims: Record<string, unknown>
  /**
   * How the token was checked: `'jwks'` = locally with the project's
   * public key; `'server'` = by the gateway (`GET /v1/auth/user`).
   */
  verifiedBy: 'jwks' | 'server'
}

/** Options for `auth.verifyAccessToken()`. */
export interface VerifyAccessTokenOptions {
  /**
   * Ask the gateway (`GET /v1/auth/user`) when the environment prevents a
   * local check: no WebCrypto, or the JWKS can't be fetched. Default
   * `true`. With `false` those cases return an error.
   */
  serverFallback?: boolean
  /**
   * Accept HS256 tokens by asking the gateway about each one. Default
   * `false`: production tokens are ES256, and an HS256 header is refused
   * locally (`'HS256 tokens are not accepted'`), so forged tokens cost no
   * request. Turn it on only while a project still has HS256 tokens in
   * use (issued before ES256 signing). Ignored with `serverFallback: false`.
   */
  allowHS256?: boolean
}

export type VerifyAccessTokenResult =
  | { data: VerifiedAccessToken; error: null }
  | { data: null; error: string }

/** Issuer the gateway puts in every end-user access token. */
const ISSUER = 'eurobase'
/** Re-read the JWKS after this long (the gateway sends max-age=3600). */
const JWKS_TTL_MS = 60 * 60 * 1000
/** At most one extra JWKS fetch per this long for an unknown key id. */
const JWKS_REFETCH_COOLDOWN_MS = 60 * 1000
/** After a failed JWKS fetch, wait this long before trying again. */
const JWKS_FAILURE_BACKOFF_MS = 30 * 1000
const JWKS_FETCH_TIMEOUT_MS = 10 * 1000
/** A token the gateway refused is refused again without a request this long. */
const REFUSED_TTL_MS = 60 * 1000
const REFUSED_CACHE_MAX = 1000

/** Cache key for a token: its SHA-256 (the token itself only without WebCrypto). */
async function tokenKey(token: string): Promise<string> {
  const subtle = subtleCrypto()
  if (!subtle || typeof subtle.digest !== 'function') return token
  try {
    const digest = new Uint8Array(await subtle.digest('SHA-256', new TextEncoder().encode(token) as BufferSource))
    let hex = ''
    for (const b of digest) hex += b.toString(16).padStart(2, '0')
    return hex
  } catch {
    return token
  }
}

const INVALID = 'invalid token'
const EXPIRED = 'token expired'

/** Strip an optional `Bearer ` prefix (the raw Authorization header works too). */
export function stripBearer(token: string): string {
  const t = token.trim()
  return /^bearer\s+/i.test(t) ? t.replace(/^bearer\s+/i, '').trim() : t
}

function base64UrlToBytes(input: string): Uint8Array | null {
  if (typeof atob !== 'function' || !/^[A-Za-z0-9_-]*$/.test(input)) return null
  try {
    let b64 = input.replace(/-/g, '+').replace(/_/g, '/')
    b64 += '='.repeat((4 - (b64.length % 4)) % 4)
    const bin = atob(b64)
    const bytes = new Uint8Array(bin.length)
    for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i)
    return bytes
  } catch {
    return null
  }
}

function decodeJson(part: string): Record<string, unknown> | null {
  const bytes = base64UrlToBytes(part)
  if (!bytes || typeof TextDecoder === 'undefined') return null
  try {
    const value = JSON.parse(new TextDecoder().decode(bytes))
    return value && typeof value === 'object' && !Array.isArray(value) ? value : null
  } catch {
    return null
  }
}

function subtleCrypto(): SubtleCrypto | null {
  const c = (globalThis as { crypto?: Crypto }).crypto
  return c && c.subtle && typeof c.subtle.verify === 'function' ? c.subtle : null
}

interface JwksCache {
  keys: Map<string, CryptoKey>
  /** The only key when the JWKS has exactly one (for tokens without kid). */
  single: CryptoKey | null
  fetchedAt: number
}

/** Verifies end-user access tokens; caches the project's JWKS. */
export class AccessTokenVerifier {
  private config: EurobaseConfig
  private http: HttpClient
  private cache: JwksCache | null = null
  private loading: Promise<JwksCache | null> | null = null
  private lastFetchAttempt = 0
  private lastFailure = 0
  /** Tokens the gateway refused recently (by hash), so repeats cost no request. */
  private refused = new Map<string, { until: number; error: string }>()

  constructor(config: EurobaseConfig, http: HttpClient) {
    this.config = config
    this.http = http
  }

  get jwksUrl(): string {
    return `${this.config.url.replace(/\/+$/, '')}/v1/auth/.well-known/jwks.json`
  }

  async verify(rawToken: string, options?: VerifyAccessTokenOptions): Promise<VerifyAccessTokenResult> {
    if (typeof rawToken !== 'string' || !rawToken) return { data: null, error: 'missing token' }
    const token = stripBearer(rawToken)
    const parts = token.split('.')
    if (parts.length !== 3) return { data: null, error: INVALID }
    const header = decodeJson(parts[0])
    const claims = decodeJson(parts[1])
    if (!header || !claims) return { data: null, error: INVALID }
    const fallback = options?.serverFallback !== false

    if (header.alg === 'HS256') {
      // Signed with the project's secret: only the gateway can check it,
      // and only on request (see allowHS256).
      return fallback && options?.allowHS256 ? this.verifyWithServer(token, claims) : { data: null, error: 'HS256 tokens are not accepted' }
    }
    if (header.alg !== 'ES256') return { data: null, error: INVALID }

    const subtle = subtleCrypto()
    if (!subtle) {
      return fallback ? this.verifyWithServer(token, claims) : { data: null, error: 'WebCrypto is not available in this runtime' }
    }

    const kid = typeof header.kid === 'string' ? header.kid : null
    let key = await this.findKey(kid, false)
    if (key === undefined) {
      // JWKS unavailable (network, 404 when the URL isn't the project's own).
      return fallback ? this.verifyWithServer(token, claims) : { data: null, error: 'could not load the project JWKS' }
    }
    if (key === null) {
      // Unknown key id: re-read the JWKS once in a while (never per token).
      key = await this.findKey(kid, true)
      if (!key) return { data: null, error: INVALID }
    }

    const signature = base64UrlToBytes(parts[2])
    if (!signature || signature.length !== 64) return { data: null, error: INVALID }
    let valid = false
    try {
      valid = await subtle.verify(
        { name: 'ECDSA', hash: 'SHA-256' },
        key,
        signature as BufferSource,
        new TextEncoder().encode(`${parts[0]}.${parts[1]}`) as BufferSource,
      )
    } catch {
      valid = false
    }
    if (!valid) return { data: null, error: INVALID }

    const claimError = this.checkClaims(claims, true)
    if (claimError) return { data: null, error: claimError }
    return { data: toVerified(claims, 'jwks'), error: null }
  }

  /**
   * The registered claims, as the gateway checks them, plus the issuer and
   * audience it sets on ES256 tokens.
   */
  private checkClaims(claims: Record<string, unknown>, local: boolean): string | null {
    if (claims.type !== 'enduser') return INVALID
    if (typeof claims.sub !== 'string' || !claims.sub) return INVALID
    if (typeof claims.exp !== 'number') return INVALID
    if (claims.exp <= Math.floor(Date.now() / 1000)) return EXPIRED
    if (typeof claims.nbf === 'number' && claims.nbf > Math.floor(Date.now() / 1000)) return INVALID
    if (!local) return null
    if (claims.iss !== ISSUER) return INVALID
    const aud = claims.aud
    const auds = typeof aud === 'string' ? [aud] : Array.isArray(aud) ? aud.filter((a) => typeof a === 'string') : []
    if (auds.length === 0) return INVALID
    const projectId = this.config.projectId
    if (projectId && !auds.includes(projectId)) return INVALID
    if (typeof claims.project_id === 'string' && !auds.includes(claims.project_id)) return INVALID
    return null
  }

  private async verifyWithServer(token: string, claims: Record<string, unknown>): Promise<VerifyAccessTokenResult> {
    // Cheap checks first: no request for an expired or foreign-looking token.
    const claimError = this.checkClaims(claims, false)
    if (claimError) return { data: null, error: claimError }
    const key = await tokenKey(token)
    const refused = this.refused.get(key)
    if (refused && refused.until > Date.now()) return { data: null, error: refused.error }
    const result = await this.http.get('/v1/auth/user', undefined, { accessToken: token })
    let error: string | null = null
    if (result?.error) {
      // 401 = bad token; 403 = account_suspended; 404 = user deleted.
      if (result.status === 401 || result.status === 403 || result.status === 404) {
        error = result.status === 401 ? INVALID : String(result.error)
      } else {
        return { data: null, error: String(result.error) } // temporary: not cached
      }
    } else if (!result || typeof result.id !== 'string' || result.id !== claims.sub) {
      error = INVALID
    }
    if (error) {
      this.rememberRefusal(key, error)
      return { data: null, error }
    }
    const verified = toVerified(claims, 'server')
    if (typeof result.email === 'string') verified.email = result.email
    return { data: verified, error: null }
  }

  private rememberRefusal(key: string, error: string) {
    const now = Date.now()
    if (this.refused.size >= REFUSED_CACHE_MAX) {
      for (const [k, v] of this.refused) {
        if (v.until <= now) this.refused.delete(k)
      }
      // Still full: drop the oldest entries (Map keeps insertion order).
      for (const k of this.refused.keys()) {
        if (this.refused.size < REFUSED_CACHE_MAX) break
        this.refused.delete(k)
      }
    }
    this.refused.set(key, { until: now + REFUSED_TTL_MS, error })
  }

  /**
   * The key for `kid`: a CryptoKey, `null` when the JWKS was loaded but
   * has no such key, `undefined` when the JWKS can't be loaded.
   */
  private async findKey(kid: string | null, refetch: boolean): Promise<CryptoKey | null | undefined> {
    const now = Date.now()
    let cache = this.cache
    const stale = !cache || now - cache.fetchedAt > JWKS_TTL_MS
    const mayRefetch = refetch && now - this.lastFetchAttempt > JWKS_REFETCH_COOLDOWN_MS
    if ((stale || mayRefetch) && now - this.lastFailure > JWKS_FAILURE_BACKOFF_MS) {
      // A failed fetch keeps the keys we have: the project's key never
      // changes on its own (it's derived from the project's signing secret).
      cache = (await this.load()) ?? this.cache
    }
    if (!cache) return undefined
    if (kid === null) return cache.single
    return cache.keys.get(kid) ?? null
  }

  private load(): Promise<JwksCache | null> {
    if (!this.loading) {
      this.lastFetchAttempt = Date.now()
      this.loading = this.fetchJwks().finally(() => {
        this.loading = null
      })
    }
    return this.loading
  }

  private async fetchJwks(): Promise<JwksCache | null> {
    const subtle = subtleCrypto()
    if (!subtle) return null
    try {
      const init: RequestInit = { method: 'GET', headers: { accept: 'application/json' } }
      if (typeof AbortSignal !== 'undefined' && typeof AbortSignal.timeout === 'function') {
        init.signal = AbortSignal.timeout(JWKS_FETCH_TIMEOUT_MS)
      }
      const res = await fetch(this.jwksUrl, init)
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      const body = await res.json()
      const list: unknown[] = Array.isArray(body?.keys) ? body.keys : []
      const keys = new Map<string, CryptoKey>()
      let single: CryptoKey | null = null
      let count = 0
      for (const jwk of list as Array<Record<string, unknown>>) {
        if (!jwk || jwk.kty !== 'EC' || jwk.crv !== 'P-256') continue
        if (jwk.alg !== undefined && jwk.alg !== 'ES256') continue
        if (jwk.use !== undefined && jwk.use !== 'sig') continue
        if (typeof jwk.x !== 'string' || typeof jwk.y !== 'string') continue
        try {
          const key = await subtle.importKey(
            'jwk',
            { kty: 'EC', crv: 'P-256', x: jwk.x, y: jwk.y, ext: true },
            { name: 'ECDSA', namedCurve: 'P-256' },
            false,
            ['verify'],
          )
          count++
          single = key
          if (typeof jwk.kid === 'string') keys.set(jwk.kid, key)
        } catch {
          // Skip a key WebCrypto won't import.
        }
      }
      if (count === 0) throw new Error('no usable key')
      const cache: JwksCache = { keys, single: count === 1 ? single : null, fetchedAt: Date.now() }
      this.cache = cache
      return cache
    } catch {
      this.lastFailure = Date.now()
      return null
    }
  }
}

function toVerified(claims: Record<string, unknown>, verifiedBy: 'jwks' | 'server'): VerifiedAccessToken {
  return {
    userId: claims.sub as string,
    email: typeof claims.email === 'string' ? claims.email : '',
    role: 'authenticated',
    expiresAt: claims.exp as number,
    claims,
    verifiedBy,
  }
}
