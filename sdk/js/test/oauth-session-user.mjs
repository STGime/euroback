// #720: after an OAuth sign-in the session's user must carry the id and
// email from the access token right away (not '' until the next refresh),
// and getUser() writes the full profile back into the session.
//
// Run via: npm run build && node test/oauth-session-user.mjs

import { createClient } from '../dist/index.js'
import assert from 'node:assert/strict'

const b64url = (o) => Buffer.from(JSON.stringify(o)).toString('base64url')
const jwt = (claims) => `${b64url({ alg: 'HS256', typ: 'JWT' })}.${b64url(claims)}.sig`

const USER_ID = '6f1c2d3e-4b5a-4c6d-8e7f-001122334455'
const EMAIL = 'zoë@example.com' // non-ASCII: claims are UTF-8
const access = jwt({ sub: USER_ID, email: EMAIL, type: 'enduser', exp: 9999999999 })

// Minimal browser globals for handleOAuthCallback.
const store = new Map()
globalThis.localStorage = {
  getItem: (k) => store.get(k) ?? null,
  setItem: (k, v) => store.set(k, String(v)),
  removeItem: (k) => store.delete(k),
}
let replaced = null
globalThis.window = {
  location: {
    search: '',
    hash: `#access_token=${access}&refresh_token=rt-1&token_type=bearer&expires_in=3600`,
    pathname: '/auth/callback',
  },
  history: { replaceState: (_s, _t, url) => { replaced = url } },
}

let nextUser = null
globalThis.fetch = async () => ({
  ok: true, status: 200, statusText: 'OK',
  headers: { get: (k) => (k.toLowerCase() === 'content-type' ? 'application/json' : null) },
  async json() { return nextUser },
})

const eb = createClient({ url: 'https://example.eurobase.app', apiKey: 'eb_pk_test' })
const events = []
eb.auth.onAuthStateChange((e, s) => events.push([e, s?.user.id]))

const { data, error } = eb.auth.handleOAuthCallback()
assert.equal(error, null)
assert.equal(data.user.id, USER_ID, 'returned session has the user id')
assert.equal(data.user.email, EMAIL, 'returned session has the (UTF-8) email')
assert.equal(eb.auth.getSession().user.id, USER_ID, 'getSession() has the user id')
assert.deepEqual(events.at(-1), ['SIGNED_IN', USER_ID], 'SIGNED_IN carries the user')
assert.equal(JSON.parse(store.get('eurobase_auth_session')).user.id, USER_ID, 'persisted with the user')
assert.equal(replaced, '/auth/callback', 'hash cleaned')

// getUser() fills the rest of the profile into the session (same id only).
nextUser = { id: USER_ID, email: EMAIL, display_name: 'Zoë', created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-02T00:00:00Z' }
await eb.auth.getUser()
assert.equal(eb.auth.getSession().user.display_name, 'Zoë')
assert.equal(eb.auth.getSession().user.created_at, '2026-01-01T00:00:00Z')
assert.equal(JSON.parse(store.get('eurobase_auth_session')).user.display_name, 'Zoë', 'profile persisted')

// A different user from the server never replaces the session's user.
nextUser = { id: 'someone-else', email: 'x@example.com', created_at: '', updated_at: '' }
await eb.auth.getUser()
assert.equal(eb.auth.getSession().user.id, USER_ID)

// A session stored by SDK ≤ 0.8.0 after an OAuth sign-in (empty user) is
// repaired on restore.
store.set('eurobase_auth_session', JSON.stringify({
  access_token: access, token_type: 'bearer', expires_in: 3600, refresh_token: 'rt-2',
  user: { id: '', email: '', created_at: '', updated_at: '' },
}))
const eb2 = createClient({ url: 'https://example.eurobase.app', apiKey: 'eb_pk_test' })
assert.equal(eb2.auth.getSession().user.id, USER_ID, 'restored empty-user session repaired')

// A token that isn't a JWT leaves the user empty rather than throwing.
window.location.hash = '#access_token=opaque&refresh_token=rt-3'
const eb3 = createClient({ url: 'https://example.eurobase.app', apiKey: 'eb_pk_test' })
store.clear()
const r3 = eb3.auth.handleOAuthCallback()
assert.equal(r3.error, null)
assert.equal(r3.data.user.id, '')

console.log('oauth-session-user: ok')
process.exit(0)
