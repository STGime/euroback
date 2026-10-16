// #804: the refresh timer must never exceed setTimeout's 32-bit limit
// (a 30-day session fired at once and refreshed in a loop), and a session
// restored from storage must refresh before its real expiry, not
// expires_in after the reload.
//
// Run via: npm run build && node test/refresh-timer.mjs

import { createClient } from '../dist/index.js'
import assert from 'node:assert/strict'

const MAX = 2 ** 31 - 1
const b64url = (o) => Buffer.from(JSON.stringify(o)).toString('base64url')
const jwt = (claims) => `${b64url({ alg: 'HS256', typ: 'JWT' })}.${b64url(claims)}.sig`
const nowS = () => Math.floor(Date.now() / 1000)

// Record timers instead of running them.
let timers = []
const fire = (t) => { timers = timers.filter((x) => x !== t); t.fn() }
globalThis.setTimeout = (fn, ms) => { const t = { fn, ms, unrefed: false, unref() { t.unrefed = true } }; timers.push(t); return t }
globalThis.clearTimeout = (t) => { timers = timers.filter((x) => x !== t) }

const store = new Map()
globalThis.localStorage = {
  getItem: (k) => store.get(k) ?? null,
  setItem: (k, v) => store.set(k, String(v)),
  removeItem: (k) => store.delete(k),
}

let refreshCalls = 0
let nextSession = null
globalThis.fetch = async (url) => {
  if (String(url).endsWith('/v1/auth/refresh')) refreshCalls++
  return {
    ok: true, status: 200, statusText: 'OK',
    headers: { get: (k) => (k.toLowerCase() === 'content-type' ? 'application/json' : null) },
    async json() { return nextSession },
  }
}

const session = (expiresIn, extra = {}) => ({
  access_token: jwt({ sub: 'u1', email: 'a@example.com', exp: nowS() + expiresIn }),
  token_type: 'bearer', expires_in: expiresIn, refresh_token: 'rt',
  user: { id: 'u1', email: 'a@example.com', created_at: '', updated_at: '' },
  ...extra,
})
const near = (actual, expected, msg) => assert.ok(Math.abs(actual - expected) < 2000, `${msg}: ${actual} vs ${expected}`)
const client = () => createClient({ url: 'https://example.eurobase.app', apiKey: 'eb_pk_test' })

// 1. A 30-day session: the timer is capped, and firing it re-arms instead of refreshing.
{
  timers = []; store.clear(); refreshCalls = 0
  nextSession = session(30 * 86400)
  const eb = client()
  const { data, error } = await eb.auth.signIn({ email: 'a@example.com', password: 'pw' })
  assert.equal(error, null)
  near(data.expires_at * 1000, Date.now() + 30 * 86400 * 1000, 'returned session has expires_at')
  assert.equal(timers.length, 1)
  assert.equal(timers[0].ms, MAX, 'capped at setTimeout max')
  fire(timers[0])
  assert.equal(refreshCalls, 0, 'no refresh when the capped timer fires early')
  assert.equal(timers.length, 1, 're-armed')
  assert.ok(timers[0].ms <= MAX && timers[0].ms > 0)
  assert.ok(timers[0].unrefed, 're-armed timer is unref()ed (no Node hang)')
  const stored = JSON.parse(store.get('eurobase_auth_session'))
  near(stored.expires_at * 1000, Date.now() + 30 * 86400 * 1000, 'expires_at stored')
  await eb.auth.signOut()
  assert.equal(timers.length, 0, 'signOut cancels the pending capped timer')
  assert.equal(store.get('eurobase_auth_session'), undefined, 'signOut removes the stored session')
}

// 2. A 1-hour session: refresh 60 s before expiry, and it does refresh.
{
  timers = []; store.clear(); refreshCalls = 0
  nextSession = session(3600)
  const eb = client()
  await eb.auth.signIn({ email: 'a@example.com', password: 'pw' })
  near(timers[0].ms, 3540 * 1000, '1 h session')
  fire(timers[0])
  await new Promise((r) => setImmediate(r))
  assert.equal(refreshCalls, 1, 'refreshes when due')
}

// 3. Restored session with expires_at 10 min ahead: refresh in ~9 min, not ~59.
{
  timers = []; store.clear()
  store.set('eurobase_auth_session', JSON.stringify(session(3600, { expires_at: nowS() + 600 })))
  client()
  near(timers[0].ms, 540 * 1000, 'restored with expires_at')
}

// 4. Session stored by an older SDK (no expires_at): use the token's exp.
{
  timers = []; store.clear()
  const s = session(3600)
  s.access_token = jwt({ sub: 'u1', exp: nowS() + 300 })
  store.set('eurobase_auth_session', JSON.stringify(s))
  client()
  near(timers[0].ms, 240 * 1000, 'restored from exp claim')
}

// 5. Restored session already past expiry: refresh soon (5 s floor), no loop.
{
  timers = []; store.clear()
  store.set('eurobase_auth_session', JSON.stringify(session(3600, { expires_at: nowS() - 100 })))
  client()
  assert.equal(timers[0].ms, 5000)
}

console.log('refresh-timer: ok')
