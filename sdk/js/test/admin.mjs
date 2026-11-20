// eb.auth.admin — the user admin API for servers (secret key only).
// Asserts URLs, methods, bodies and headers, the { data, error } shape,
// and that a public-key client fails without sending anything. Stubbed
// fetch, no gateway.
//
// Run via: npm run build && node test/admin.mjs

import { createClient, ADMIN_NEEDS_SECRET_KEY } from '../dist/index.js'
import assert from 'node:assert/strict'

const URL_BASE = 'https://my-app.eurobase.app'
const ID = '6f486f53-2841-4a86-961b-9673dfb2b58d'

let calls = []
let nextResponse = null

function reset(response) {
  calls = []
  nextResponse = response
}

globalThis.fetch = async (url, init) => {
  let body
  if (init && typeof init.body === 'string') body = JSON.parse(init.body)
  calls.push({ url, method: init?.method ?? 'GET', headers: init?.headers ?? {}, body })
  const { status = 200, json = {}, statusText = 'OK' } = nextResponse ?? {}
  const text = status === 204 ? '' : JSON.stringify(json)
  return {
    ok: status >= 200 && status < 300,
    status,
    statusText,
    headers: { get: (k) => (k.toLowerCase() === 'content-type' ? 'application/json' : null) },
    async text() { return text },
    async json() { return JSON.parse(text) },
  }
}

const user = {
  id: ID, email: 'ana@partner.example', display_name: 'Ana', providers: ['email'],
  metadata: { company: 'Partner GmbH' }, banned_at: null, last_sign_in_at: null,
  email_confirmed_at: '2026-11-20T00:00:00Z', created_at: '2026-11-20T00:00:00Z',
}

const eb = createClient({ url: URL_BASE + '/', apiKey: 'eb_sk_test' })
const base = `${URL_BASE}/v1/auth/admin/users`

// ── createUser: snake_case body, apikey header, no Authorization ────
{
  reset({ status: 201, json: user })
  const res = await eb.auth.admin.createUser({
    email: 'ana@partner.example',
    password: 'Partner-pass-1',
    emailConfirmed: true,
    displayName: 'Ana',
    metadata: { company: 'Partner GmbH' },
  })
  assert.equal(calls.length, 1)
  assert.equal(calls[0].method, 'POST')
  assert.equal(calls[0].url, base)
  assert.equal(calls[0].headers.apikey, 'eb_sk_test')
  assert.equal(calls[0].headers.Authorization, undefined)
  assert.deepEqual(calls[0].body, {
    email: 'ana@partner.example', password: 'Partner-pass-1', email_confirmed: true,
    display_name: 'Ana', metadata: { company: 'Partner GmbH' },
  })
  assert.deepEqual(res, { data: user, error: null })
}

// Only the fields given are sent.
{
  reset({ status: 201, json: user })
  await eb.auth.admin.createUser({ email: 'b@example.com' })
  assert.deepEqual(calls[0].body, { email: 'b@example.com' })
}

// ── 409 duplicate → error + status + code ───────────────────────────
{
  reset({ status: 409, statusText: 'Conflict', json: { error: 'a user with this email already exists', code: 'email_exists' } })
  const res = await eb.auth.admin.createUser({ email: 'ana@partner.example' })
  assert.deepEqual(res, { data: null, error: 'a user with this email already exists', status: 409, code: 'email_exists' })
}

// ── listUsers: page / per_page / search ─────────────────────────────
{
  reset({ json: { users: [user], total: 1 } })
  const res = await eb.auth.admin.listUsers({ page: 2, perPage: 25, search: 'partner' })
  assert.equal(calls[0].method, 'GET')
  assert.equal(calls[0].url, `${base}?page=2&per_page=25&search=partner`)
  assert.equal(calls[0].body, undefined)
  assert.equal(res.data.total, 1)
  reset({ json: { users: [], total: 0 } })
  await eb.auth.admin.listUsers()
  assert.equal(calls[0].url, base)
}

// ── getUserById / 404 ───────────────────────────────────────────────
{
  reset({ json: user })
  const res = await eb.auth.admin.getUserById(ID)
  assert.equal(calls[0].url, `${base}/${ID}`)
  assert.equal(res.data.id, ID)
  reset({ status: 404, statusText: 'Not Found', json: { error: 'user not found' } })
  const nf = await eb.auth.admin.getUserById('x/../y')
  assert.equal(calls[0].url, `${base}/x%2F..%2Fy`)
  assert.deepEqual(nf, { data: null, error: 'user not found', status: 404 })
}

// ── updateUserById ──────────────────────────────────────────────────
{
  reset({ json: user })
  await eb.auth.admin.updateUserById(ID, { displayName: 'Ana B', emailConfirmed: false, metadata: { a: 1 } })
  assert.equal(calls[0].method, 'PATCH')
  assert.equal(calls[0].url, `${base}/${ID}`)
  assert.deepEqual(calls[0].body, { display_name: 'Ana B', metadata: { a: 1 }, email_confirmed: false })
}

// ── deleteUser: 204 → { data: null, error: null } ───────────────────
{
  reset({ status: 204 })
  const res = await eb.auth.admin.deleteUser(ID)
  assert.equal(calls[0].method, 'DELETE')
  assert.equal(calls[0].url, `${base}/${ID}`)
  assert.deepEqual(res, { data: null, error: null })
}

// ── suspend / unsuspend / setPassword / signOutUser ─────────────────
{
  const cases = [
    ['suspendUser', [ID], 'POST', '/suspend', undefined],
    ['unsuspendUser', [ID], 'DELETE', '/suspend', undefined],
    ['setPassword', [ID, 'New-pass-123'], 'POST', '/password', { password: 'New-pass-123' }],
    ['signOutUser', [ID], 'POST', '/signout', undefined],
  ]
  for (const [method, args, httpMethod, suffix, body] of cases) {
    reset({ json: { status: 'ok' } })
    const res = await eb.auth.admin[method](...args)
    assert.equal(calls[0].method, httpMethod, method)
    assert.equal(calls[0].url, `${base}/${ID}${suffix}`, method)
    assert.deepEqual(calls[0].body, body, method)
    assert.equal(res.error, null, method)
  }
}

// ── A public-key client never sends a request ───────────────────────
{
  const pub = createClient({ url: URL_BASE, apiKey: 'eb_pk_public' })
  reset({ json: user })
  for (const call of [
    () => pub.auth.admin.listUsers(),
    () => pub.auth.admin.createUser({ email: 'x@example.com' }),
    () => pub.auth.admin.deleteUser(ID),
    () => pub.auth.admin.signOutUser(ID),
  ]) {
    const res = await call()
    assert.equal(res.data, null)
    assert.equal(res.error, ADMIN_NEEDS_SECRET_KEY)
    assert.equal(res.code, 'secret_key_required')
  }
  assert.equal(calls.length, 0, 'no request with a public key')
}

// ── A signed-in end user's token is never sent with admin calls ─────
{
  reset({ json: { access_token: 'user.jwt.token', refresh_token: 'r', user: { id: 'u' }, expires_in: 3600 } })
  await eb.auth.signIn({ email: 'u@example.com', password: 'pw-123456' })
  reset({ json: user })
  await eb.auth.admin.getUserById(ID)
  assert.equal(calls[0].headers.Authorization, undefined)
  await eb.auth.signOut().catch(() => {})
}

// ── Network failure → error, no throw ───────────────────────────────
{
  const saved = globalThis.fetch
  globalThis.fetch = async () => { throw new Error('connect ECONNREFUSED') }
  const res = await eb.auth.admin.listUsers()
  assert.deepEqual(res, { data: null, error: 'connect ECONNREFUSED' })
  globalThis.fetch = saved
}

console.log('admin: all assertions passed')
