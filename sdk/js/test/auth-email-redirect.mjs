// Asserts the #259 changes:
//   - signUp / requestMagicLink / forgotPassword / resendVerification
//     forward `emailRedirectTo` as `email_redirect_to` on the wire
//     (backend uses snake_case; SDK reads camelCase).
//   - When the option is OMITTED, the wire body must NOT contain
//     `email_redirect_to` at all (the backend distinguishes "absent"
//     from "empty string" in ResolveEmailRedirect).
//   - The three new completion methods (verifyEmail, resetPassword,
//     resendVerification) hit their endpoints with the right shape.
//
// Run via: npm run build && node test/auth-email-redirect.mjs

import { createClient } from '../dist/index.js'
import assert from 'node:assert/strict'

const URL_BASE = 'https://newtek2.eurobase.app'

let captured = null
let nextResponse = null

function reset(response) {
  captured = null
  nextResponse = response
}

globalThis.fetch = async (url, init) => {
  let bodyJson
  if (init && typeof init.body === 'string') {
    try { bodyJson = JSON.parse(init.body) } catch { bodyJson = init.body }
  }
  captured = {
    url,
    method: init?.method ?? 'GET',
    body: bodyJson,
  }
  const { status = 200, body = {}, headers = { 'content-type': 'application/json' } } = nextResponse ?? {}
  return {
    ok: status >= 200 && status < 300,
    status,
    statusText: `HTTP ${status}`,
    headers: { get: (k) => headers[k.toLowerCase()] ?? null },
    async json() { return body },
  }
}

const eb = createClient({ url: URL_BASE, apiKey: 'eb_pk_test' })

// ── signUp: sends email_redirect_to when set, omits it when not ─────
{
  reset({
    status: 200,
    body: {
      access_token: 'at', token_type: 'bearer', expires_in: 3600, refresh_token: 'rt',
      user: { id: 'u1', email: 'e@x.com', created_at: 'now', updated_at: 'now' },
    },
  })
  await eb.auth.signUp({
    email: 'e@x.com',
    password: 'SecurePass123!',
    emailRedirectTo: 'https://app.example.com/verify',
  })
  assert.equal(captured.url, `${URL_BASE}/v1/auth/signup`)
  assert.equal(captured.body.email_redirect_to, 'https://app.example.com/verify',
    'signUp did not forward emailRedirectTo as snake_case')
  assert.equal(captured.body.emailRedirectTo, undefined,
    'signUp leaked the camelCase field on the wire')
}
{
  reset({
    status: 200,
    body: {
      access_token: 'at', token_type: 'bearer', expires_in: 3600, refresh_token: 'rt',
      user: { id: 'u1', email: 'e@x.com', created_at: 'now', updated_at: 'now' },
    },
  })
  await eb.auth.signUp({ email: 'e@x.com', password: 'SecurePass123!' })
  assert.ok(
    !('email_redirect_to' in (captured.body ?? {})),
    'signUp added email_redirect_to when caller did not pass one — backend cannot distinguish absent from empty',
  )
}

// ── forgotPassword: same round-trip ─────────────────────────────────
{
  reset({ status: 200, body: { status: 'ok' } })
  await eb.auth.forgotPassword('e@x.com', { emailRedirectTo: 'https://app.example.com/reset' })
  assert.equal(captured.url, `${URL_BASE}/v1/auth/forgot-password`)
  assert.equal(captured.body.email, 'e@x.com')
  assert.equal(captured.body.email_redirect_to, 'https://app.example.com/reset')
}
{
  reset({ status: 200, body: { status: 'ok' } })
  await eb.auth.forgotPassword('e@x.com')
  assert.ok(!('email_redirect_to' in (captured.body ?? {})),
    'forgotPassword added email_redirect_to when caller did not pass one')
}

// ── requestMagicLink: same round-trip ───────────────────────────────
{
  reset({ status: 200, body: { status: 'ok' } })
  await eb.auth.requestMagicLink('e@x.com', { emailRedirectTo: 'https://app.example.com/magic' })
  assert.equal(captured.url, `${URL_BASE}/v1/auth/request-magic-link`)
  assert.equal(captured.body.email_redirect_to, 'https://app.example.com/magic')
}
{
  reset({ status: 200, body: { status: 'ok' } })
  await eb.auth.requestMagicLink('e@x.com')
  assert.ok(!('email_redirect_to' in (captured.body ?? {})),
    'requestMagicLink added email_redirect_to when caller did not pass one')
}

// ── resendVerification: same round-trip ─────────────────────────────
{
  reset({ status: 200, body: { status: 'ok' } })
  await eb.auth.resendVerification('e@x.com', { emailRedirectTo: 'https://app.example.com/verify' })
  assert.equal(captured.url, `${URL_BASE}/v1/auth/resend-verification`)
  assert.equal(captured.body.email_redirect_to, 'https://app.example.com/verify')
}
{
  reset({ status: 200, body: { status: 'ok' } })
  await eb.auth.resendVerification('e@x.com')
  assert.ok(!('email_redirect_to' in (captured.body ?? {})),
    'resendVerification added email_redirect_to when caller did not pass one')
}

// ── verifyEmail: hits POST /v1/auth/verify-email with {token} ───────
{
  reset({ status: 200, body: { status: 'ok' } })
  const { error } = await eb.auth.verifyEmail('opaque-hex-token')
  assert.equal(error, null)
  assert.equal(captured.url, `${URL_BASE}/v1/auth/verify-email`)
  assert.equal(captured.body.token, 'opaque-hex-token')
}

// ── resetPassword: hits POST /v1/auth/reset-password with token + new_password ──
{
  reset({ status: 200, body: { status: 'ok' } })
  const { error } = await eb.auth.resetPassword('opaque-hex-token', 'NewPass123!')
  assert.equal(error, null)
  assert.equal(captured.url, `${URL_BASE}/v1/auth/reset-password`)
  assert.equal(captured.body.token, 'opaque-hex-token')
  // Snake case — the backend expects new_password, not newPassword.
  assert.equal(captured.body.new_password, 'NewPass123!',
    'resetPassword did not send new_password (backend expects snake_case)')
  assert.equal(captured.body.newPassword, undefined,
    'resetPassword leaked camelCase newPassword on the wire')
}

console.log('OK: auth email-redirect + completion methods')
