// #808: updatePassword sends current + new, stores the fresh session and
// emits TOKEN_REFRESHED; an error leaves the session as it was.
//
// Run via: npm run build && node test/update-password.mjs

import { createClient } from '../dist/index.js'
import assert from 'node:assert/strict'

const store = new Map()
globalThis.localStorage = {
  getItem: (k) => store.get(k) ?? null,
  setItem: (k, v) => store.set(k, String(v)),
  removeItem: (k) => store.delete(k),
}
globalThis.setTimeout = () => ({ unref() {} })

let next, lastReq
globalThis.fetch = async (url, init) => {
  lastReq = { url: String(url), body: init?.body ? JSON.parse(init.body) : null, auth: init?.headers?.Authorization ?? init?.headers?.authorization }
  return {
    ok: next.status < 400, status: next.status, statusText: '',
    headers: { get: (k) => (k.toLowerCase() === 'content-type' ? 'application/json' : null) },
    async json() { return next.body },
  }
}

const user = { id: 'u1', email: 'a@example.com', created_at: '', updated_at: '' }
const session = (tok) => ({ access_token: tok, token_type: 'bearer', expires_in: 3600, refresh_token: 'rt-' + tok, user })
const eb = createClient({ url: 'https://example.eurobase.app', apiKey: 'eb_pk_test' })

// Not signed in.
let res = await eb.auth.updatePassword({ currentPassword: 'a', newPassword: 'b' })
assert.equal(res.error, 'not signed in')

next = { status: 200, body: session('t1') }
await eb.auth.signIn({ email: 'a@example.com', password: 'old-password-1' })
const events = []
eb.auth.onAuthStateChange((e) => events.push(e))
events.length = 0

// Wrong current password: error, session unchanged.
next = { status: 400, body: { error: 'current password is incorrect' } }
res = await eb.auth.updatePassword({ currentPassword: 'nope', newPassword: 'new-password-2' })
assert.equal(res.error, 'current password is incorrect')
assert.equal(eb.auth.getSession().access_token, 't1')
assert.deepEqual(events, [])

// Success: request shape, new session stored, TOKEN_REFRESHED.
next = { status: 200, body: session('t2') }
res = await eb.auth.updatePassword({ currentPassword: 'old-password-1', newPassword: 'new-password-2' })
assert.equal(res.error, null)
assert.ok(lastReq.url.endsWith('/v1/auth/user/password'))
assert.deepEqual(lastReq.body, { current_password: 'old-password-1', new_password: 'new-password-2' })
assert.equal(res.data.access_token, 't2')
assert.equal(eb.auth.getSession().refresh_token, 'rt-t2')
assert.equal(JSON.parse(store.get('eurobase_auth_session')).access_token, 't2')
assert.deepEqual(events, ['TOKEN_REFRESHED'])

console.log('update-password: ok')
