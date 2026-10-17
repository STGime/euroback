// #806: with email confirmation required, signUp returns the user and no
// session (confirmationRequired), stores nothing, and a later signIn's
// 403 email_not_confirmed comes back as that error string.
//
// Run via: npm run build && node test/email-confirmation.mjs

import { createClient, EMAIL_NOT_CONFIRMED } from '../dist/index.js'
import assert from 'node:assert/strict'

const store = new Map()
globalThis.localStorage = {
  getItem: (k) => store.get(k) ?? null,
  setItem: (k, v) => store.set(k, String(v)),
  removeItem: (k) => store.delete(k),
}
let timers = 0
globalThis.setTimeout = () => { timers++; return { unref() {} } }

let next = { status: 201, body: null }
globalThis.fetch = async () => ({
  ok: next.status < 400, status: next.status, statusText: '',
  headers: { get: (k) => (k.toLowerCase() === 'content-type' ? 'application/json' : null) },
  async json() { return next.body },
})

const user = { id: 'u1', email: 'teen@example.com', email_confirmed_at: null, created_at: '', updated_at: '' }
const eb = createClient({ url: 'https://example.eurobase.app', apiKey: 'eb_pk_test' })
const events = []
eb.auth.onAuthStateChange((e) => events.push(e))

// Pending sign-up.
next = { status: 201, body: { user, confirmation_required: true } }
const up = await eb.auth.signUp({ email: user.email, password: 'correct-horse-1' })
assert.equal(up.error, null)
assert.equal(up.data, null, 'no session')
assert.equal(up.confirmationRequired, true)
assert.equal(up.user.id, 'u1')
assert.equal(eb.auth.getSession(), null, 'nothing stored in memory')
assert.equal(store.size, 0, 'nothing persisted')
assert.equal(timers, 0, 'no refresh timer')
assert.deepEqual(events, [], 'no SIGNED_IN')

// Sign-in before confirming.
next = { status: 403, body: { error: 'email_not_confirmed' } }
const inn = await eb.auth.signIn({ email: user.email, password: 'correct-horse-1' })
assert.equal(inn.error, EMAIL_NOT_CONFIRMED)
assert.equal(inn.data, null)

// A project without the setting: signUp signs in as before.
next = { status: 201, body: { access_token: 'a.b.c', token_type: 'bearer', expires_in: 3600, refresh_token: 'rt', user: { ...user, email_confirmed_at: '2026-10-17T00:00:00Z' } } }
const open = await eb.auth.signUp({ email: user.email, password: 'correct-horse-1' })
assert.equal(open.confirmationRequired, false)
assert.equal(open.data.access_token, 'a.b.c')
assert.equal(open.user.email_confirmed_at, '2026-10-17T00:00:00Z')
assert.deepEqual(events, ['SIGNED_IN'])

console.log('email-confirmation: ok')
