// Server-side use: server mode (auth.persistSession: false), withAccessToken(),
// getUser(token) and verifyAccessToken() against a JWKS.
//
// Run via: npm run build && node test/server-side.mjs

import { createClient } from '../dist/index.js'
import assert from 'node:assert/strict'

const URL_BASE = 'https://proj.eurobase.app'
const PUBLIC_KEY = 'eb_pk_test'
const SECRET_KEY = 'eb_sk_test'
const PROJECT_ID = '11111111-2222-3333-4444-555555555555'

// ── Test helpers ────────────────────────────────────────────────────

const b64url = (bytes) => Buffer.from(bytes).toString('base64url')
const enc = (obj) => b64url(new TextEncoder().encode(JSON.stringify(obj)))

const keyPair = await crypto.subtle.generateKey({ name: 'ECDSA', namedCurve: 'P-256' }, true, ['sign', 'verify'])
const otherPair = await crypto.subtle.generateKey({ name: 'ECDSA', namedCurve: 'P-256' }, true, ['sign', 'verify'])
const jwk = await crypto.subtle.exportKey('jwk', keyPair.publicKey)
const KID = 'kid-1'

async function es256(claims, { kid = KID, key = keyPair.privateKey, header = {} } = {}) {
  const h = enc({ alg: 'ES256', typ: 'JWT', kid, ...header })
  const p = enc(claims)
  const sig = await crypto.subtle.sign({ name: 'ECDSA', hash: 'SHA-256' }, key, new TextEncoder().encode(`${h}.${p}`))
  return `${h}.${p}.${b64url(new Uint8Array(sig))}`
}

function hs256(claims) {
  return `${enc({ alg: 'HS256', typ: 'JWT' })}.${enc(claims)}.c2lnbmF0dXJl`
}

const now = () => Math.floor(Date.now() / 1000)
const goodClaims = (extra = {}) => ({
  sub: 'user-1', email: 'u@example.com', project_id: PROJECT_ID, type: 'enduser',
  iss: 'eurobase', aud: PROJECT_ID, iat: now(), exp: now() + 600, ...extra,
})

let jwksResponse = () => ({ status: 200, body: { keys: [{ ...jwk, kid: KID, alg: 'ES256', use: 'sig' }] } })
let userResponse = () => ({ status: 200, body: { id: 'user-1', email: 'server@example.com', created_at: '', updated_at: '' } })
const calls = []

const json = (status, body) => new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } })
const session = (token = 'at-1', refresh = 'rt-1') => ({
  access_token: token, token_type: 'bearer', expires_in: 3600, refresh_token: refresh,
  user: { id: 'user-1', email: 'u@example.com', created_at: '', updated_at: '' },
})

globalThis.fetch = async (url, init = {}) => {
  const u = String(url)
  calls.push({ url: u, method: init.method ?? 'GET', headers: { ...(init.headers ?? {}) }, body: init.body })
  if (u.endsWith('/v1/auth/.well-known/jwks.json')) {
    const r = jwksResponse()
    return json(r.status, r.body)
  }
  if (u.endsWith('/v1/auth/user')) {
    const r = userResponse()
    return json(r.status, r.body)
  }
  if (u.endsWith('/v1/auth/signin')) return json(200, session())
  if (u.endsWith('/v1/auth/signup')) return json(200, session('at-up', 'rt-up'))
  if (u.endsWith('/v1/auth/refresh')) return json(200, session('at-2', 'rt-2'))
  if (u.endsWith('/v1/auth/signout')) return json(200, { status: 'ok' })
  if (u.endsWith('/v1/auth/user/password')) return json(200, session('at-pw', 'rt-pw'))
  if (u.includes('/v1/storage')) return json(200, { objects: [], has_more: false })
  if (u.endsWith('/v1/db/dupes')) return json(409, { error: 'a record with this day already exists' })
  return json(200, { data: [], count: 0 })
}

const authOf = (pred) => {
  const c = calls.filter(pred).at(-1)
  assert.ok(c, `no request matched; got ${calls.map((x) => x.url).join(', ')}`)
  return c.headers.Authorization
}

// A fake localStorage, to prove server mode never writes to it.
const store = new Map()
globalThis.localStorage = {
  getItem: (k) => (store.has(k) ? store.get(k) : null),
  setItem: (k, v) => store.set(k, String(v)),
  removeItem: (k) => store.delete(k),
}

const warnings = []
const origWarn = console.warn
console.warn = (...args) => warnings.push(args.join(' '))

// ── 1. Server mode keeps no session ────────────────────────────────
{
  const eb = createClient({ url: URL_BASE, apiKey: PUBLIC_KEY, auth: { persistSession: false } })
  const events = []
  eb.auth.onAuthStateChange((e) => events.push(e))

  const { data, error } = await eb.auth.signIn({ email: 'u@example.com', password: 'pw' })
  assert.equal(error, null)
  assert.equal(data.access_token, 'at-1', 'signIn still returns the session')
  assert.equal(typeof data.expires_at, 'number', 'returned session has expires_at')
  assert.equal(eb.auth.getSession(), null, 'server mode: no session kept')
  assert.deepEqual(events, [], 'server mode: no auth events')
  assert.equal(store.size, 0, 'server mode: nothing written to localStorage')

  const up = await eb.auth.signUp({ email: 'n@example.com', password: 'pw' })
  assert.equal(up.data.access_token, 'at-up')
  assert.equal(eb.auth.getSession(), null)

  calls.length = 0
  await eb.db.from('todos').select('id')
  assert.equal(authOf((c) => c.url.includes('/v1/db/todos')), undefined, 'shared server client sends no user token')

  // Refresh needs the token on a server; the result isn't kept either.
  const noArg = await eb.auth.refreshSession()
  assert.match(noArg.error, /no refresh token/)
  const r = await eb.auth.refreshSession('rt-1')
  assert.equal(r.error, null)
  assert.equal(r.data.refresh_token, 'rt-2')
  assert.equal(JSON.parse(calls.at(-1).body).refresh_token, 'rt-1')
  const r2 = await eb.auth.refreshSession({ refresh_token: 'rt-2' })
  assert.equal(r2.error, null)
  assert.equal(eb.auth.getSession(), null)

  // Sign-out revokes the given token.
  calls.length = 0
  const so = await eb.auth.signOut('rt-2')
  assert.equal(so.error, null)
  assert.equal(JSON.parse(calls.find((c) => c.url.endsWith('/v1/auth/signout')).body).refresh_token, 'rt-2')
  assert.match((await eb.auth.signOut()).error, /no refresh token/)

  // updatePassword on the shared client: no user.
  assert.equal((await eb.auth.updatePassword({ currentPassword: 'a', newPassword: 'b' })).error, 'not signed in')
  assert.deepEqual(events, [])
  assert.equal(warnings.length, 0, 'explicit persistSession: no warning')
}

// ── 2. withAccessToken: per-request user client ─────────────────────
{
  const eb = createClient({ url: URL_BASE, apiKey: PUBLIC_KEY, auth: { persistSession: false } })
  const tokenA = await es256(goodClaims({ sub: 'user-a' }))
  const tokenB = await es256(goodClaims({ sub: 'user-b' }))
  const a = eb.withAccessToken(tokenA)
  const b = eb.withAccessToken(`Bearer ${tokenB}`)
  assert.equal(b.accessToken, tokenB, 'Bearer prefix stripped')

  calls.length = 0
  await Promise.all([a.db.from('todos').select('id'), b.db.from('todos').select('id'), eb.db.from('todos').select('id')])
  const dbAuth = calls.filter((c) => c.url.includes('/v1/db/todos')).map((c) => c.headers.Authorization).sort()
  assert.deepEqual(dbAuth, [undefined, `Bearer ${tokenA}`, `Bearer ${tokenB}`].sort(), 'each client sends its own token')

  calls.length = 0
  await a.storage.list()
  assert.equal(authOf((c) => c.url.includes('/v1/storage')), `Bearer ${tokenA}`)
  await a.functions.invoke('hello', { body: { x: 1 } })
  assert.equal(authOf((c) => c.url.includes('/v1/functions/hello')), `Bearer ${tokenA}`)
  await a.db.rpc('fn', {})
  assert.equal(authOf((c) => c.url.includes('/v1/db/rpc/fn')), `Bearer ${tokenA}`)

  // Realtime uses the user's token; project_id comes from the token.
  const ws = new URL(a.realtime.buildWebSocketURL())
  assert.equal(ws.searchParams.get('token'), tokenA)
  assert.equal(ws.searchParams.get('project_id'), PROJECT_ID)
  // The shared client's realtime still uses the API key.
  assert.equal(new URL(eb.realtime.buildWebSocketURL()).searchParams.get('token'), PUBLIC_KEY)

  // getUser() on the user client asks for that user; updatePassword works and returns a session.
  calls.length = 0
  await a.auth.getUser()
  assert.equal(authOf((c) => c.url.endsWith('/v1/auth/user')), `Bearer ${tokenA}`)
  const pw = await a.auth.updatePassword({ currentPassword: 'old', newPassword: 'new' })
  assert.equal(pw.error, null)
  assert.equal(pw.data.access_token, 'at-pw')
  assert.equal(authOf((c) => c.url.endsWith('/v1/auth/user/password')), `Bearer ${tokenA}`)
  // The user client keeps no session and still sends the original token.
  assert.equal(a.auth.getSession(), null)
  calls.length = 0
  await a.db.from('todos').select('id')
  assert.equal(authOf((c) => c.url.includes('/v1/db/todos')), `Bearer ${tokenA}`)

  // Secret-key clients refuse: the service role would skip RLS.
  const admin = createClient({ url: URL_BASE, apiKey: SECRET_KEY, auth: { persistSession: false } })
  assert.throws(() => admin.withAccessToken(tokenA), /public key/)
  assert.throws(() => eb.withAccessToken(''), /needs an access token/)
}

// ── 3. getUser(token) ───────────────────────────────────────────────
{
  const eb = createClient({ url: URL_BASE, apiKey: PUBLIC_KEY, auth: { persistSession: false } })
  calls.length = 0
  const { data, error } = await eb.auth.getUser('Bearer tok-x')
  assert.equal(error, null)
  assert.equal(data.id, 'user-1')
  assert.equal(authOf((c) => c.url.endsWith('/v1/auth/user')), 'Bearer tok-x')
  assert.equal((await eb.auth.getUser('')).error, 'missing token')
}

// ── 4. verifyAccessToken: ES256 via JWKS ───────────────────────────
{
  const eb = createClient({ url: URL_BASE, apiKey: PUBLIC_KEY, projectId: PROJECT_ID, auth: { persistSession: false } })
  calls.length = 0
  const token = await es256(goodClaims())
  const { data, error } = await eb.auth.verifyAccessToken(token)
  assert.equal(error, null)
  assert.equal(data.userId, 'user-1')
  assert.equal(data.email, 'u@example.com')
  assert.equal(data.role, 'authenticated')
  assert.equal(data.verifiedBy, 'jwks')
  assert.equal(data.claims.project_id, PROJECT_ID)
  assert.equal(typeof data.expiresAt, 'number')

  // Cached: a second verification fetches nothing; no server call at all.
  await eb.auth.verifyAccessToken(`Bearer ${token}`)
  assert.equal(calls.filter((c) => c.url.endsWith('jwks.json')).length, 1, 'JWKS fetched once')
  assert.equal(calls.filter((c) => c.url.endsWith('/v1/auth/user')).length, 0, 'no server call for ES256')
  assert.equal(calls.find((c) => c.url.endsWith('jwks.json')).headers.apikey, undefined, 'JWKS fetched without API key')

  // The user client shares the main client's cache.
  await eb.withAccessToken(token).auth.verifyAccessToken(token)
  assert.equal(calls.filter((c) => c.url.endsWith('jwks.json')).length, 1, 'user client shares the JWKS cache')

  const bad = async (t, msg, expected = 'invalid token') => {
    const r = await eb.auth.verifyAccessToken(t)
    assert.equal(r.data, null, msg)
    assert.equal(r.error, expected, msg)
  }
  await bad(await es256(goodClaims({ exp: now() - 5 })), 'expired', 'token expired')
  await bad(await es256(goodClaims({ aud: 'other-project' })), 'wrong audience')
  await bad(await es256(goodClaims({ aud: undefined })), 'missing audience')
  await bad(await es256(goodClaims({ iss: 'someone' })), 'wrong issuer')
  await bad(await es256(goodClaims({ type: 'platform' })), 'not an end-user token')
  await bad(await es256(goodClaims({ project_id: 'other' })), 'project_id differs from aud')
  // Another project's token whose aud and project_id agree: only the
  // configured projectId catches it.
  await bad(await es256(goodClaims({ aud: 'other', project_id: 'other' })), 'audience is not the configured project')
  await bad(await es256(goodClaims({ nbf: now() + 300 })), 'not valid yet (nbf)')
  await bad(await es256(goodClaims(), { key: otherPair.privateKey }), 'signed with another key')
  const parts = token.split('.')
  await bad(`${parts[0]}.${enc({ ...goodClaims(), sub: 'attacker' })}.${parts[2]}`, 'tampered payload')
  await bad(`${enc({ alg: 'none' })}.${parts[1]}.`, 'alg none')
  await bad('not-a-jwt', 'garbage')
  assert.equal((await eb.auth.verifyAccessToken('')).error, 'missing token')
  assert.equal(calls.filter((c) => c.url.endsWith('/v1/auth/user')).length, 0, 'bad ES256 tokens never reach the server')

  // Unknown kid: refused; the JWKS is re-read at most once a minute.
  const jwksCount = () => calls.filter((c) => c.url.endsWith('jwks.json')).length
  const before = jwksCount()
  await bad(await es256(goodClaims(), { kid: 'unknown' }), 'unknown kid')
  assert.equal(jwksCount(), before, 'no refetch right after a fetch')
  const realNow = Date.now
  Date.now = () => realNow() + 61_000
  try {
    await bad(await es256(goodClaims({ exp: now() + 600 }), { kid: 'unknown' }), 'unknown kid after cooldown')
    await bad(await es256(goodClaims({ exp: now() + 600 }), { kid: 'unknown-2' }), 'unknown kid again')
    assert.equal(jwksCount(), before + 1, 'unknown kid refetches at most once per cooldown')
  } finally {
    Date.now = realNow
  }
}

// ── 5. verifyAccessToken: fallbacks ────────────────────────────────
{
  // HS256: refused locally by default — a forged HS256 header costs no request.
  const eb = createClient({ url: URL_BASE, apiKey: PUBLIC_KEY, auth: { persistSession: false } })
  calls.length = 0
  const hs = hs256(goodClaims({ aud: undefined }))
  const def = await eb.auth.verifyAccessToken(hs)
  assert.equal(def.data, null)
  assert.equal(def.error, 'HS256 tokens are not accepted')
  assert.equal(calls.length, 0, 'HS256 is not sent to the gateway by default')

  // Opt-in: the gateway decides.
  const opt = { allowHS256: true }
  const r = await eb.auth.verifyAccessToken(hs, opt)
  assert.equal(r.error, null)
  assert.equal(r.data.verifiedBy, 'server')
  assert.equal(r.data.userId, 'user-1')
  assert.equal(r.data.email, 'server@example.com')
  assert.equal(authOf((c) => c.url.endsWith('/v1/auth/user')), `Bearer ${hs}`)

  const off = await eb.auth.verifyAccessToken(hs, { ...opt, serverFallback: false })
  assert.equal(off.data, null)
  assert.match(off.error, /HS256/)

  // An expired HS256 token is refused without a request.
  calls.length = 0
  assert.equal((await eb.auth.verifyAccessToken(hs256(goodClaims({ exp: now() - 1 })), opt)).error, 'token expired')
  assert.equal(calls.length, 0)

  // Server says 401 → invalid, and the refusal is remembered: a repeat costs no request.
  const hs401 = hs256(goodClaims({ aud: undefined, sub: 'user-401' }))
  userResponse = () => ({ status: 401, body: { error: 'invalid or expired token' } })
  calls.length = 0
  assert.equal((await eb.auth.verifyAccessToken(hs401, opt)).error, 'invalid token')
  assert.equal((await eb.auth.verifyAccessToken(hs401, opt)).error, 'invalid token')
  assert.equal(calls.filter((c) => c.url.endsWith('/v1/auth/user')).length, 1, 'refused token cached')
  // Suspended (403 account_suspended) → that error, also cached.
  const hsBanned = hs256(goodClaims({ aud: undefined, sub: 'user-banned' }))
  userResponse = () => ({ status: 403, body: { error: 'account_suspended', code: 'account_suspended' } })
  assert.equal((await eb.auth.verifyAccessToken(hsBanned, opt)).error, 'account_suspended')
  // A temporary gateway error is not cached.
  const hs500 = hs256(goodClaims({ aud: undefined, sub: 'user-500' }))
  userResponse = () => ({ status: 503, body: { error: 'unavailable' } })
  calls.length = 0
  await eb.auth.verifyAccessToken(hs500, opt)
  await eb.auth.verifyAccessToken(hs500, opt)
  assert.equal(calls.filter((c) => c.url.endsWith('/v1/auth/user')).length, 2, '5xx not cached')
  // Server returns another user than the token names → invalid.
  userResponse = () => ({ status: 200, body: { id: 'someone-else', email: '' } })
  assert.equal((await eb.auth.verifyAccessToken(hs256(goodClaims({ aud: undefined, sub: 'user-x' })), opt)).error, 'invalid token')
  userResponse = () => ({ status: 200, body: { id: 'user-1', email: 'server@example.com' } })
}
{
  // getUser(token) passes on the gateway's suspension error.
  const eb = createClient({ url: URL_BASE, apiKey: PUBLIC_KEY, auth: { persistSession: false } })
  userResponse = () => ({ status: 403, body: { error: 'account_suspended', code: 'account_suspended' } })
  const r = await eb.auth.getUser('tok-banned')
  assert.equal(r.data, null)
  assert.equal(r.error, 'account_suspended')
  userResponse = () => ({ status: 200, body: { id: 'user-1', email: 'server@example.com' } })
}
{
  // QueryResult carries the HTTP status: 409 = unique violation.
  const eb = createClient({ url: URL_BASE, apiKey: SECRET_KEY, auth: { persistSession: false } })
  const r = await eb.db.from('dupes').insert({ day: '2026-11-20' })
  assert.equal(r.status, 409)
  assert.match(r.error, /already exists/)
}
{
  // Realtime on a withAccessToken client stops once its token has expired.
  const created = []
  const RealWS = globalThis.WebSocket
  globalThis.WebSocket = class { static OPEN = 1; static CONNECTING = 0; constructor(url) { created.push(url) } close() {} }
  try {
    const eb = createClient({ url: URL_BASE, apiKey: PUBLIC_KEY, auth: { persistSession: false } })
    const expired = eb.withAccessToken(await es256(goodClaims({ exp: now() - 10 })))
    const errors = []
    expired.realtime.onError((e) => errors.push(e))
    expired.realtime.on('todos', '*', () => {})
    assert.equal(created.length, 0, 'no connection with an expired token')
    assert.equal(errors.length, 1)
    assert.match(errors[0], /expired/)
    const live = eb.withAccessToken(await es256(goodClaims()))
    live.realtime.on('todos', '*', () => {})
    assert.equal(created.length, 1, 'a live token connects')
  } finally {
    globalThis.WebSocket = RealWS
  }
}
{
  // JWKS not available (e.g. url isn't the project's own subdomain) → gateway.
  jwksResponse = () => ({ status: 404, body: { error: 'not found' } })
  const eb = createClient({ url: URL_BASE, apiKey: PUBLIC_KEY, auth: { persistSession: false } })
  calls.length = 0
  const token = await es256(goodClaims())
  const r = await eb.auth.verifyAccessToken(token)
  assert.equal(r.error, null)
  assert.equal(r.data.verifiedBy, 'server')
  const off = await eb.auth.verifyAccessToken(token, { serverFallback: false })
  assert.equal(off.data, null)
  assert.match(off.error, /JWKS/)
  // A failed fetch isn't retried on every call (backoff).
  assert.equal(calls.filter((c) => c.url.endsWith('jwks.json')).length, 1)
}

// ── 6. Default (kept session) in a server runtime warns once ────────
{
  // Not in a browser Web Worker / extension service worker (one user).
  warnings.length = 0
  globalThis.WorkerGlobalScope = class {}
  globalThis.importScripts = () => {}
  try {
    const w = createClient({ url: URL_BASE, apiKey: PUBLIC_KEY })
    await w.auth.signIn({ email: 'u@example.com', password: 'pw' })
    assert.equal(warnings.length, 0, 'no warning in a Web Worker')
    await w.auth.signOut()
  } finally {
    delete globalThis.WorkerGlobalScope
    delete globalThis.importScripts
  }
}
{
  warnings.length = 0
  const eb = createClient({ url: URL_BASE, apiKey: PUBLIC_KEY })
  await eb.auth.signIn({ email: 'u@example.com', password: 'pw' })
  assert.equal(eb.auth.getSession()?.access_token, 'at-1', 'default behaviour unchanged: session kept')
  calls.length = 0
  await eb.db.from('todos').select('id')
  assert.equal(authOf((c) => c.url.includes('/v1/db/todos')), 'Bearer at-1')
  assert.equal(warnings.length, 1, 'warns once in a server runtime')
  assert.match(warnings[0], /persistSession: false/)
  const eb2 = createClient({ url: URL_BASE, apiKey: PUBLIC_KEY })
  await eb2.auth.signIn({ email: 'u@example.com', password: 'pw' })
  assert.equal(warnings.length, 1, 'only once per process')
  await eb.auth.signOut()
  await eb2.auth.signOut()
}

// ── 7. persistSession: true + autoRefreshToken: false → no timer ────
{
  const eb = createClient({ url: URL_BASE, apiKey: PUBLIC_KEY, auth: { persistSession: true, autoRefreshToken: false } })
  await eb.auth.signIn({ email: 'u@example.com', password: 'pw' })
  assert.equal(eb.auth.getSession()?.access_token, 'at-1')
  assert.equal(eb.auth.refreshTimer, null, 'no refresh scheduled')
  // An explicit refresh token in persist mode also updates the kept session.
  const r = await eb.auth.refreshSession('rt-1')
  assert.equal(r.data.access_token, 'at-2')
  assert.equal(eb.auth.getSession()?.access_token, 'at-2')
  await eb.auth.signOut()
}

console.warn = origWarn
console.log('server-side: ok')
