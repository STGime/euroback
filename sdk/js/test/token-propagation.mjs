// Regression test for issue #38: AuthClient must share its HttpClient with
// DatabaseClient and StorageClient so that the Authorization: Bearer <token>
// header reaches RLS-protected endpoints.
//
// Run via: npm run build && node test/token-propagation.mjs

import { createClient } from '../dist/index.js'
import assert from 'node:assert/strict'

const FAKE_TOKEN = 'eyJ-fake-jwt-token'
const FAKE_API_KEY = 'eb_pk_fake'
const FAKE_URL = 'https://fake.eurobase.app'

const calls = []

globalThis.fetch = async (url, init = {}) => {
  const headers = init.headers ?? {}
  calls.push({ url: String(url), method: init.method ?? 'GET', headers: { ...headers } })

  if (String(url).endsWith('/v1/auth/signin')) {
    return new Response(
      JSON.stringify({
        access_token: FAKE_TOKEN,
        token_type: 'bearer',
        expires_in: 3600,
        refresh_token: 'rt-fake',
        user: { id: 'u1', email: 'a@b.c', created_at: '', updated_at: '' },
      }),
      { status: 200, headers: { 'content-type': 'application/json' } },
    )
  }

  return new Response(JSON.stringify({ data: [], count: 0 }), {
    status: 200,
    headers: { 'content-type': 'application/json' },
  })
}

function authHeaderFor(predicate) {
  const call = calls.find(predicate)
  assert.ok(call, `expected request matching predicate; got: ${JSON.stringify(calls.map(c => c.url))}`)
  return call.headers.Authorization
}

const eb = createClient({ url: FAKE_URL, apiKey: FAKE_API_KEY })

// Pre-signIn: queries should NOT carry an Authorization header.
await eb.db.from('todos').select('id')
const preDbAuth = authHeaderFor(c => c.url.includes('/v1/db/todos'))
assert.equal(preDbAuth, undefined, 'pre-signIn db request should not carry Authorization header')

// Sign in — token enters AuthClient's http instance.
const { error } = await eb.auth.signIn({ email: 'a@b.c', password: 'pw' })
assert.equal(error, null, 'signIn should succeed against the fake gateway')

// Reset call log so we only inspect post-signIn traffic.
calls.length = 0

// Post-signIn: db query must carry the bearer token.
await eb.db.from('todos').select('id')
const dbAuth = authHeaderFor(c => c.url.includes('/v1/db/todos'))
assert.equal(dbAuth, `Bearer ${FAKE_TOKEN}`, 'db query must carry shared bearer token after signIn')

// Post-signIn: storage list must also carry the bearer token.
await eb.storage.list()
const storageAuth = authHeaderFor(c => c.url.includes('/v1/storage') && !c.url.includes('/v1/storage/upload'))
assert.equal(storageAuth, `Bearer ${FAKE_TOKEN}`, 'storage request must carry shared bearer token after signIn')

// Sign out clears the token from the shared instance.
await eb.auth.signOut()
calls.length = 0
await eb.db.from('todos').select('id')
const postSignOutDbAuth = authHeaderFor(c => c.url.includes('/v1/db/todos'))
assert.equal(postSignOutDbAuth, undefined, 'db request after signOut must not carry Authorization header')

console.log('token-propagation.mjs: PASS')
