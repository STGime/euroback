// Email language (#1007 follow-up): the SDK sends `locale` only when the
// app asks for it (client `locale`, or a call's option), never on its own.
//
// Run via: npm run build && node test/auth-locale.mjs

import { createClient } from '../dist/index.js'
import assert from 'node:assert/strict'

const URL_BASE = 'https://newtek2.eurobase.app'
let captured = []

globalThis.fetch = async (url, init) => {
  let body
  if (init && typeof init.body === 'string') {
    try { body = JSON.parse(init.body) } catch { body = init.body }
  }
  captured.push({ url: String(url), method: init?.method ?? 'GET', body })
  return {
    ok: true, status: 200, statusText: 'OK',
    headers: { get: (k) => (k.toLowerCase() === 'content-type' ? 'application/json' : null) },
    json: async () => ({ user: { id: 'u1', email: 'a@example.com' }, status: 'ok' }),
    text: async () => '{}',
  }
}

async function bodiesFor(client) {
  captured = []
  await client.auth.signUp({ email: 'a@example.com', password: 'correct-horse-1' })
  await client.auth.resendVerification('a@example.com')
  await client.auth.forgotPassword('a@example.com')
  await client.auth.requestMagicLink('a@example.com')
  return captured.map((c) => c.body)
}

// 1. No locale configured: nothing is sent (behaviour unchanged).
for (const b of await bodiesFor(createClient({ url: URL_BASE, apiKey: 'eb_pk_test' }))) {
  assert.equal('locale' in b, false, `no locale configured must send none: ${JSON.stringify(b)}`)
}

// 2. A fixed client locale goes with every auth-email request.
for (const b of await bodiesFor(createClient({ url: URL_BASE, apiKey: 'eb_pk_test', locale: 'de' }))) {
  assert.equal(b.locale, 'de')
}

// 3. "auto" on a server sends nothing (the server's language isn't the
//    user's), even where navigator.language exists (Node 21+, Deno).
Object.defineProperty(globalThis, 'navigator', { value: { language: 'sv-SE' }, configurable: true })
for (const b of await bodiesFor(createClient({ url: URL_BASE, apiKey: 'eb_pk_test', locale: 'auto' }))) {
  assert.equal('locale' in b, false, `auto on a server must send none: ${JSON.stringify(b)}`)
}
// ...and on a user's device (a browser) the device's language.
globalThis.document = {}
for (const b of await bodiesFor(createClient({ url: URL_BASE, apiKey: 'eb_pk_test', locale: 'auto' }))) {
  assert.equal(b.locale, 'sv-SE')
}
delete globalThis.document

// 4. A call's own locale wins over the client's.
captured = []
const c = createClient({ url: URL_BASE, apiKey: 'eb_pk_test', locale: 'de' })
await c.auth.signUp({ email: 'a@example.com', password: 'correct-horse-1', locale: 'fr' })
await c.auth.forgotPassword('a@example.com', { locale: 'nb-NO' })
assert.equal(captured[0].body.locale, 'fr')
assert.equal(captured[1].body.locale, 'nb-NO')

// 5. Admin API: locale on create and update; "" clears.
captured = []
const admin = createClient({ url: URL_BASE, apiKey: 'eb_sk_test', auth: { persistSession: false } }).auth.admin
await admin.createUser({ email: 'b@example.com', locale: 'es' })
await admin.updateUserById('11111111-1111-1111-1111-111111111111', { locale: '' })
await admin.updateUserById('11111111-1111-1111-1111-111111111111', { displayName: 'B' })
assert.equal(captured[0].body.locale, 'es')
assert.equal(captured[1].body.locale, '')
assert.equal('locale' in captured[2].body, false)

console.log('auth-locale: ok')
