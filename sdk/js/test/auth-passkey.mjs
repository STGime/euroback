// Asserts the end-user passkey helpers (#630) drive the two-step WebAuthn
// ceremonies and hit the right gateway endpoints:
//   registerPasskey() → /v1/auth/passkey/register/{begin,finish}
//   signInWithPasskey() → /v1/auth/passkey/login/{begin,finish} → session
//   listPasskeys()    → GET /v1/auth/passkey
//   deletePasskey(id) → DELETE /v1/auth/passkey/{id}
//
// Both `fetch` and `navigator.credentials` are stubbed so the test runs
// without a real gateway or authenticator.
//
// Run via: npm run build && node test/auth-passkey.mjs

import assert from 'node:assert/strict'

// --- minimal browser globals so passkeysSupported() is true and the
//     webauthn helpers can run in Node. Must be set BEFORE importing the SDK. ---
globalThis.window = globalThis
globalThis.PublicKeyCredential = function () {}

let credCall = null
// Node ships a read-only built-in `navigator`, so define over it instead
// of assigning. We only need `.credentials` for the webauthn helpers.
Object.defineProperty(globalThis, 'navigator', {
  configurable: true,
  value: {
    credentials: {
      async create(opts) {
        credCall = { kind: 'create', opts }
        return fakeCredential('attestationObject')
      },
      async get(opts) {
        credCall = { kind: 'get', opts }
        return fakeCredential('assertion')
      },
    },
  },
})

function buf(str) {
  const bytes = new TextEncoder().encode(str)
  return bytes.buffer
}

// A fake PublicKeyCredential shaped like what createPasskey/getPasskeyAssertion read.
function fakeCredential(kind) {
  const response =
    kind === 'attestationObject'
      ? {
          clientDataJSON: buf('cdj'),
          attestationObject: buf('att'),
          getTransports: () => ['internal'],
        }
      : {
          clientDataJSON: buf('cdj'),
          authenticatorData: buf('ad'),
          signature: buf('sig'),
          userHandle: buf('uh'),
        }
  return {
    id: 'cred-id-1',
    rawId: buf('rawid'),
    type: 'public-key',
    authenticatorAttachment: 'platform',
    getClientExtensionResults: () => ({}),
    response,
  }
}

const { createClient } = await import('../dist/index.js')

const URL_BASE = 'https://newtek2.eurobase.app'

let captured = []
let responses = []

globalThis.fetch = async (url, init) => {
  let bodyJson
  if (init && typeof init.body === 'string') {
    try { bodyJson = JSON.parse(init.body) } catch { bodyJson = init.body }
  }
  captured.push({ url, method: init?.method ?? 'GET', body: bodyJson })
  const { status = 200, body = {} } = responses.shift() ?? {}
  return {
    ok: status >= 200 && status < 300,
    status,
    statusText: `HTTP ${status}`,
    headers: { get: (k) => (k.toLowerCase() === 'content-type' ? 'application/json' : null) },
    async json() { return body },
  }
}

function reset(resList) {
  captured = []
  responses = resList
  credCall = null
}

const eb = createClient({ url: URL_BASE, apiKey: 'eb_pk_test' })
eb.auth.setSession({
  access_token: 'end-user-jwt',
  token_type: 'bearer',
  expires_in: 3600,
  refresh_token: 'rt-fake',
  user: { id: 'u1', email: 'alex@example.com', created_at: '', updated_at: '' },
})

// --- passkeysSupported() ---
assert.equal(eb.auth.passkeysSupported(), true, 'passkeysSupported true with window+navigator')

// --- registerPasskey() ---
const beginOptions = { publicKey: { challenge: 'Y2hhbA', user: { id: 'dXNlcg', name: 'alex' } } }
reset([
  { body: { challenge_id: 'reg-chal-1', options: beginOptions } },
  { body: { id: 'pk-1', nickname: 'MacBook', created_at: '2026-10-06T10:00:00Z', last_used_at: null, backed_up: true } },
])
const reg = await eb.auth.registerPasskey('MacBook')
assert.equal(reg.error, null, 'registerPasskey no error')
assert.equal(reg.data.id, 'pk-1')
assert.equal(reg.data.nickname, 'MacBook')
assert.equal(reg.data.backed_up, true)
assert.equal(captured[0].url, `${URL_BASE}/v1/auth/passkey/register/begin`, 'register begin path')
assert.equal(captured[1].url, `${URL_BASE}/v1/auth/passkey/register/finish`, 'register finish path')
assert.equal(credCall.kind, 'create', 'navigator.credentials.create was called')
// finish body carries the challenge id, nickname, and the encoded credential
assert.equal(captured[1].body.challenge_id, 'reg-chal-1')
assert.equal(captured[1].body.nickname, 'MacBook')
assert.equal(captured[1].body.credential.id, 'cred-id-1')
assert.equal(captured[1].body.credential.type, 'public-key')
assert.ok(captured[1].body.credential.response.attestationObject, 'attestationObject present')
assert.equal(typeof captured[1].body.credential.rawId, 'string', 'rawId base64url-encoded to a string')

// --- signInWithPasskey() → session + SIGNED_IN ---
eb.auth.signOut && (await eb.auth.signOut().catch(() => {}))
reset([
  { body: { challenge_id: 'login-chal-1', options: { publicKey: { challenge: 'Y2hhbA' } } } },
  {
    body: {
      access_token: 'new-jwt',
      token_type: 'bearer',
      expires_in: 3600,
      refresh_token: 'rt-new',
      user: { id: 'u1', email: 'alex@example.com', created_at: '', updated_at: '' },
    },
  },
])
let signedInEvent = null
const unsub = eb.auth.onAuthStateChange((evt) => { if (evt === 'SIGNED_IN') signedInEvent = evt })
const login = await eb.auth.signInWithPasskey()
assert.equal(login.error, null, 'signInWithPasskey no error')
assert.equal(login.data.access_token, 'new-jwt')
assert.equal(eb.auth.getSession()?.access_token, 'new-jwt', 'session stored')
assert.equal(signedInEvent, 'SIGNED_IN', 'SIGNED_IN emitted')
assert.equal(captured[0].url, `${URL_BASE}/v1/auth/passkey/login/begin`)
assert.equal(captured[1].url, `${URL_BASE}/v1/auth/passkey/login/finish`)
assert.equal(credCall.kind, 'get', 'navigator.credentials.get was called')
assert.equal(captured[1].body.challenge_id, 'login-chal-1')
assert.ok(captured[1].body.credential.response.signature, 'assertion signature present')
unsub()

// --- listPasskeys() ---
reset([{ body: { passkeys: [{ id: 'pk-1', nickname: 'MacBook', created_at: 'x', last_used_at: null, backed_up: true }] } }])
const list = await eb.auth.listPasskeys()
assert.equal(list.error, null)
assert.equal(list.data.length, 1)
assert.equal(list.data[0].id, 'pk-1')
assert.equal(captured[0].method, 'GET')
assert.equal(captured[0].url, `${URL_BASE}/v1/auth/passkey`)

// empty list comes back as [] not null
reset([{ body: { passkeys: [] } }])
const emptyList = await eb.auth.listPasskeys()
assert.deepEqual(emptyList.data, [], 'empty passkey list is []')

// --- deletePasskey(id) ---
reset([{ body: { status: 'ok' } }])
const del = await eb.auth.deletePasskey('pk-1')
assert.equal(del.error, null)
assert.equal(captured[0].method, 'DELETE')
assert.equal(captured[0].url, `${URL_BASE}/v1/auth/passkey/pk-1`)

// --- error passthrough: passkeys disabled on the project ---
reset([{ status: 400, body: { error: 'passkeys are not enabled for this project' } }])
const disabled = await eb.auth.signInWithPasskey()
assert.equal(disabled.data, null)
assert.equal(disabled.error, 'passkeys are not enabled for this project')
assert.equal(credCall, null, 'ceremony not attempted when begin fails')

console.log('auth-passkey.mjs: all assertions passed')
