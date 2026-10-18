// #814: session robustness.
//   1. Only a rejected refresh token (400/401/403) ends the session; a
//      network error, 429 or 5xx keeps it and retries with backoff, the
//      first retry before the access token expires.
//   2. A sign-out during a refresh stays signed out.
//   3. A response body that fails to parse is an error result, not an
//      unhandled rejection, and a non-session 200 is never stored.
//   4. Tabs share the session: storage events, adopt-before-refresh, a
//      Web Locks lock so only one tab refreshes.
//
// Run via: npm run build && node test/session-robustness.mjs

import { createClient } from '../dist/index.js'
import assert from 'node:assert/strict'

const unhandled = []
process.on('unhandledRejection', (err) => unhandled.push(err))

const b64url = (o) => Buffer.from(JSON.stringify(o)).toString('base64url')
const jwt = (claims) => `${b64url({ alg: 'HS256', typ: 'JWT' })}.${b64url(claims)}.sig`
const nowS = () => Math.floor(Date.now() / 1000)
const tick = () => new Promise((r) => setImmediate(r))
const KEY = 'eurobase_auth_session'

// Timers are recorded, not run.
let timers = []
const realSetImmediate = setImmediate
globalThis.setTimeout = (fn, ms) => { const t = { fn, ms, unref() {} }; timers.push(t); return t }
globalThis.clearTimeout = (t) => { timers = timers.filter((x) => x !== t) }
const fire = (t) => { timers = timers.filter((x) => x !== t); t.fn() }
globalThis.setImmediate = realSetImmediate

const store = new Map()
globalThis.localStorage = {
  getItem: (k) => store.get(k) ?? null,
  setItem: (k, v) => store.set(k, String(v)),
  removeItem: (k) => store.delete(k),
}

const session = (rt, expiresIn = 3600, user = 'u1') => ({
  access_token: jwt({ sub: user, exp: nowS() + expiresIn, n: rt }),
  token_type: 'bearer', expires_in: expiresIn, refresh_token: rt,
  user: { id: user, email: `${user}@example.com`, created_at: '', updated_at: '' },
})

const json = (status, body) => ({
  ok: status >= 200 && status < 300, status, statusText: String(status),
  headers: { get: (k) => (k.toLowerCase() === 'content-type' ? 'application/json' : null) },
  async json() { return body },
})

// The fetch mock: `route(url, body)` decides each response.
let route = null
const calls = []
globalThis.fetch = async (url, init) => {
  const path = new URL(String(url)).pathname
  const body = init?.body ? JSON.parse(init.body) : undefined
  calls.push({ path, body })
  return route(path, body)
}

function reset() {
  timers = []; store.clear(); calls.length = 0; route = null
}
function client() {
  return createClient({ url: 'https://example.eurobase.app', apiKey: 'eb_pk_test' })
}
function events(eb) {
  const seen = []
  eb.auth.onAuthStateChange((e) => seen.push(e))
  seen.length = 0
  return seen
}
async function signedIn(rt = 'rt1', expiresIn = 3600) {
  route = () => json(200, session(rt, expiresIn))
  const eb = client()
  const { error } = await eb.auth.signIn({ email: 'u1@example.com', password: 'pw' })
  assert.equal(error, null)
  return eb
}
const refreshCalls = () => calls.filter((c) => c.path === '/v1/auth/refresh').length

// ── 1. Temporary errors keep the session ────────────────────────────────

for (const [name, respond] of [
  ['network error', () => { throw new TypeError('fetch failed') }],
  ['503', () => json(503, { error: 'maintenance' })],
  ['500', () => json(500, { error: 'token refresh failed, try again' })],
  ['429', () => json(429, { error: 'too many requests' })],
  ['502 HTML', () => ({ ok: false, status: 502, statusText: 'Bad Gateway', headers: { get: () => 'text/html' }, async json() { throw new Error('no') } })],
]) {
  reset()
  const eb = await signedIn()
  const seen = events(eb)
  route = respond
  const { data, error } = await eb.auth.refreshSession()
  assert.equal(data, null, name)
  assert.ok(error, `${name}: error returned`)
  assert.equal(eb.auth.getSession()?.refresh_token, 'rt1', `${name}: session kept`)
  assert.ok(store.get(KEY), `${name}: stored session kept`)
  assert.deepEqual(seen, [], `${name}: no SIGNED_OUT`)
  assert.equal(timers.length, 1, `${name}: retry scheduled`)
  assert.equal(timers[0].ms, 5000, `${name}: first retry after 5 s`)
}

// Backoff doubles, capped at 60 s; a success resets it.
{
  reset()
  const eb = await signedIn('rt1', 86400)
  route = () => { throw new TypeError('offline') }
  const delays = []
  await eb.auth.refreshSession()
  for (let i = 0; i < 6; i++) {
    delays.push(timers[0].ms)
    fire(timers[0])
    await tick(); await tick()
  }
  assert.deepEqual(delays, [5000, 10000, 20000, 40000, 60000, 60000])
  route = (path) => json(200, session('rt2', 86400))
  fire(timers[0])
  await tick(); await tick()
  assert.equal(eb.auth.getSession().refresh_token, 'rt2', 'retry succeeds')
  assert.ok(timers[0].ms > 80000 * 1000, 'back to the normal schedule')
}

// Before expiry, the retry comes before the access token expires.
{
  reset()
  const eb = await signedIn('rt1', 8)
  route = () => { throw new TypeError('offline') }
  await eb.auth.refreshSession() // attempt 1: 5 s, token expires in 8 s
  fire(timers[0]); await tick(); await tick()
  // attempt 2 would wait 10 s; capped to ~7 s (expiry - 1 s).
  assert.ok(timers[0].ms <= 7000 && timers[0].ms >= 6000, `capped before expiry: ${timers[0].ms}`)
}

// A reload while offline doesn't sign the user out.
{
  reset()
  store.set(KEY, JSON.stringify({ ...session('rt1'), expires_at: nowS() - 10 }))
  route = () => { throw new TypeError('offline') }
  const eb = client()
  fire(timers[0]); await tick(); await tick()
  assert.equal(eb.auth.getSession()?.refresh_token, 'rt1')
  assert.ok(store.get(KEY))
}

// ── 1b. A rejected refresh token ends the session ───────────────────────

for (const status of [400, 401, 403]) {
  reset()
  const eb = await signedIn()
  const seen = events(eb)
  route = () => json(status, { error: status === 403 ? 'email_not_confirmed' : 'invalid or expired refresh token' })
  const { error } = await eb.auth.refreshSession()
  assert.ok(error)
  assert.equal(eb.auth.getSession(), null, `${status}: signed out`)
  assert.equal(store.get(KEY), undefined)
  assert.deepEqual(seen, ['SIGNED_OUT'])
  assert.equal(timers.length, 0, `${status}: no retry`)
}

// Concurrent refresh calls share one request.
{
  reset()
  const eb = await signedIn()
  route = () => json(200, session('rt2'))
  const [a, b] = await Promise.all([eb.auth.refreshSession(), eb.auth.refreshSession()])
  assert.equal(refreshCalls(), 1)
  assert.equal(a.data.refresh_token, 'rt2')
  assert.equal(b.data.refresh_token, 'rt2')
}

// ── 2. Sign-out during a refresh stays signed out ───────────────────────

{
  reset()
  const eb = await signedIn()
  const seen = events(eb)
  let release
  route = (path) => {
    if (path === '/v1/auth/refresh') return new Promise((r) => { release = () => r(json(200, session('rt2'))) })
    return json(200, { status: 'ok' })
  }
  const pending = eb.auth.refreshSession()
  await tick()
  await eb.auth.signOut()
  release()
  const { data } = await pending
  await tick()
  assert.equal(data, null)
  assert.equal(eb.auth.getSession(), null, 'still signed out')
  assert.equal(store.get(KEY), undefined, 'nothing stored')
  assert.deepEqual(seen, ['SIGNED_OUT'], 'no TOKEN_REFRESHED after sign-out')
  const signouts = calls.filter((c) => c.path === '/v1/auth/signout').map((c) => c.body.refresh_token)
  assert.deepEqual(signouts.sort(), ['rt1', 'rt2'], 'the token issued after sign-out is revoked too')
  assert.equal(timers.length, 0)
}

// ── 3. Unparseable bodies ───────────────────────────────────────────────

{
  reset()
  const eb = await signedIn()
  route = () => ({ ok: true, status: 200, statusText: 'OK', headers: { get: () => 'application/json' }, async json() { throw new SyntaxError('Unexpected token <') } })
  const { data, error } = await eb.auth.refreshSession()
  assert.equal(data, null)
  assert.match(error, /Unexpected token/)
  assert.equal(eb.auth.getSession().refresh_token, 'rt1', 'kept')
}
{
  // 200 that isn't JSON (a captive portal, a proxy page): not stored.
  reset()
  const eb = await signedIn()
  route = () => ({ ok: true, status: 200, statusText: 'OK', headers: { get: () => 'text/html' }, async json() { return {} } })
  const { error } = await eb.auth.refreshSession()
  assert.ok(error)
  assert.equal(eb.auth.getSession().refresh_token, 'rt1')
  assert.equal(JSON.parse(store.get(KEY)).refresh_token, 'rt1')
  // Same for sign-in.
  const eb2 = client()
  const r = await eb2.auth.signIn({ email: 'a@example.com', password: 'pw' })
  assert.equal(r.data, null)
  assert.ok(r.error)
}
{
  // From the timer: no unhandled rejection.
  reset()
  const eb = await signedIn()
  route = () => ({ ok: true, status: 200, statusText: 'OK', headers: { get: () => 'application/json' }, async json() { throw new SyntaxError('bad') } })
  fire(timers[0])
  await tick(); await tick()
  assert.equal(eb.auth.getSession().refresh_token, 'rt1')
}

// ── 4. Tabs ─────────────────────────────────────────────────────────────

// Each tab gets its own window (listeners), all share localStorage. A
// storage write fires `storage` in the other tabs, as browsers do.
const tabs = []
function tab() {
  const listeners = {}
  const win = { addEventListener: (t, fn) => { (listeners[t] ||= []).push(fn) } }
  globalThis.window = win
  const eb = client()
  delete globalThis.window
  const t = { eb, dispatch: (type, ev) => (listeners[type] || []).forEach((fn) => fn(ev)) }
  tabs.push(t)
  return t
}
function broadcastFrom(writer) {
  const value = store.get(KEY) ?? null
  for (const t of tabs) if (t !== writer) t.dispatch('storage', { key: KEY, newValue: value })
}

// Storage events: adopt a newer session; a removed one signs out.
{
  reset(); tabs.length = 0
  store.set(KEY, JSON.stringify(session('rt1')))
  const a = tab(), b = tab()
  const seenB = events(b.eb)
  store.set(KEY, JSON.stringify(session('rt2'))) // tab A refreshed
  broadcastFrom(a)
  assert.equal(b.eb.auth.getSession().refresh_token, 'rt2')
  assert.deepEqual(seenB, ['TOKEN_REFRESHED'])
  store.delete(KEY) // tab A signed out
  broadcastFrom(a)
  assert.equal(b.eb.auth.getSession(), null)
  assert.deepEqual(seenB, ['TOKEN_REFRESHED', 'SIGNED_OUT'])
  store.set(KEY, JSON.stringify(session('rt3', 3600, 'u2'))) // signed in again
  broadcastFrom(a)
  assert.equal(b.eb.auth.getSession().user.id, 'u2')
  assert.deepEqual(seenB, ['TOKEN_REFRESHED', 'SIGNED_OUT', 'SIGNED_IN'])
}

// A tab about to refresh takes another tab's newer session instead.
{
  reset(); tabs.length = 0
  store.set(KEY, JSON.stringify(session('rt1')))
  const b = tab()
  store.set(KEY, JSON.stringify(session('rt2'))) // missed event
  route = () => json(401, { error: 'invalid or expired refresh token' })
  const { data, error } = await b.eb.auth.refreshSession()
  assert.equal(error, null)
  assert.equal(data.refresh_token, 'rt2')
  assert.equal(refreshCalls(), 0, 'no refresh with the rotated token')
}

// Rejected because another tab rotated the token during the request:
// take its session, don't sign everyone out.
{
  reset(); tabs.length = 0
  store.set(KEY, JSON.stringify(session('rt1')))
  const b = tab()
  route = () => {
    store.set(KEY, JSON.stringify(session('rt2'))) // tab A won the race
    return json(401, { error: 'invalid or expired refresh token' })
  }
  const { data } = await b.eb.auth.refreshSession()
  assert.equal(data.refresh_token, 'rt2')
  assert.equal(JSON.parse(store.get(KEY)).refresh_token, 'rt2', 'stored session kept for all tabs')
}

// Web Locks: two tabs refresh at once — one request, both end on rt2.
{
  reset(); tabs.length = 0
  let held = Promise.resolve()
  Object.defineProperty(globalThis, 'navigator', {
    configurable: true,
    value: {
      locks: {
        request(name, opts, fn) {
          const run = held.then(() => fn())
          held = run.catch(() => {})
          return run
        },
      },
    },
  })
  store.set(KEY, JSON.stringify(session('rt1')))
  const a = tab(), b = tab()
  let valid = 'rt1', n = 1
  route = (path, body) => {
    if (body.refresh_token !== valid) return json(401, { error: 'invalid or expired refresh token' })
    valid = `rt${++n}`
    return json(200, session(valid))
  }
  const [ra, rb] = await Promise.all([a.eb.auth.refreshSession(), b.eb.auth.refreshSession()])
  assert.equal(refreshCalls(), 1, 'only one tab refreshed')
  assert.equal(ra.data?.refresh_token, 'rt2')
  assert.equal(rb.data?.refresh_token, 'rt2')
  assert.equal(JSON.parse(store.get(KEY)).refresh_token, 'rt2')
}

await tick()
assert.deepEqual(unhandled, [], 'no unhandled rejections')
console.log('session-robustness: ok')
