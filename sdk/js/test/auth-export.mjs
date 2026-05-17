// Asserts the new eb.auth.exportMyData() / getMyExport() helpers send
// the right HTTP requests to /v1/auth/me/export and surface the
// queued/completed ExportRequest cleanly. Uses a stubbed global fetch
// so the test runs without a real gateway.
//
// Run via: npm run build && node test/auth-export.mjs

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
    headers: init?.headers ?? {},
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

// Simulate a signed-in end-user — exportMyData requires the access
// token, set via the normal setSession path the SDK already exercises.
eb.auth.setSession({
  access_token: 'eyJ-end-user-jwt',
  token_type: 'bearer',
  expires_in: 3600,
  refresh_token: 'rt-fake',
  user: { id: 'u1', email: 'alex@example.com', created_at: '', updated_at: '' },
})

const queuedRow = {
  id: 'exp-123',
  project_id: 'proj-1',
  user_id: 'u1',
  status: 'pending',
  format: 'json',
  created_at: '2026-05-16T18:00:00Z',
}

const completedRow = {
  ...queuedRow,
  status: 'completed',
  file_size: 14823,
  download_url: 'https://s3.fr-par.scw.cloud/.../exp-123?signed=…',
  started_at: '2026-05-16T18:00:02Z',
  completed_at: '2026-05-16T18:00:09Z',
  expires_at: '2026-05-23T18:00:09Z',
}

// ── 1. exportMyData('json') — POST /v1/auth/me/export with format ──
{
  reset({ status: 202, body: queuedRow })
  const res = await eb.auth.exportMyData('json')
  assert.equal(captured.method, 'POST')
  assert.equal(captured.url, `${URL_BASE}/v1/auth/me/export`)
  assert.deepEqual(captured.body, { format: 'json' })
  assert.equal(captured.headers.Authorization, 'Bearer eyJ-end-user-jwt')
  assert.equal(res.error, null)
  assert.equal(res.data.id, 'exp-123')
  assert.equal(res.data.status, 'pending')
  console.log('✓ exportMyData("json"): POST /v1/auth/me/export with body + bearer')
}

// ── 2. exportMyData() defaults to JSON ─────────────────────────────
{
  reset({ status: 202, body: queuedRow })
  await eb.auth.exportMyData()
  assert.deepEqual(captured.body, { format: 'json' })
  console.log('✓ exportMyData(): default format is JSON')
}

// ── 3. exportMyData('csv') ─────────────────────────────────────────
{
  reset({ status: 202, body: { ...queuedRow, format: 'csv' } })
  const res = await eb.auth.exportMyData('csv')
  assert.deepEqual(captured.body, { format: 'csv' })
  assert.equal(res.data.format, 'csv')
  console.log('✓ exportMyData("csv"): forwards format')
}

// ── 4. exportMyData() — 429 rate-limit surfaces the error ──────────
{
  reset({ status: 429, body: { error: 'rate limit exceeded: 1 export per 24 hours' } })
  const res = await eb.auth.exportMyData()
  assert.equal(res.data, null)
  assert.match(res.error, /rate limit/)
  console.log('✓ exportMyData: 429 returns error.message, data=null')
}

// ── 5. getMyExport(id) — GET /v1/auth/me/export/:id ────────────────
{
  reset({ status: 200, body: queuedRow })
  const res = await eb.auth.getMyExport('exp-123')
  assert.equal(captured.method, 'GET')
  assert.equal(captured.url, `${URL_BASE}/v1/auth/me/export/exp-123`)
  assert.equal(captured.headers.Authorization, 'Bearer eyJ-end-user-jwt')
  assert.equal(res.error, null)
  assert.equal(res.data.status, 'pending')
  console.log('✓ getMyExport: GET /v1/auth/me/export/:id')
}

// ── 6. getMyExport(completedId) → completed row has download_url ───
{
  reset({ status: 200, body: completedRow })
  const res = await eb.auth.getMyExport('exp-123')
  assert.equal(res.data.status, 'completed')
  assert.equal(res.data.download_url, completedRow.download_url)
  assert.equal(res.data.file_size, 14823)
  console.log('✓ getMyExport: completed row surfaces download_url + size')
}

// ── 7. URL encoding on the export id ───────────────────────────────
{
  reset({ status: 200, body: queuedRow })
  await eb.auth.getMyExport('weird id/with/slashes')
  assert.equal(captured.url, `${URL_BASE}/v1/auth/me/export/${encodeURIComponent('weird id/with/slashes')}`)
  console.log('✓ getMyExport: id is URL-encoded')
}

// ── 8. getMyExport on a missing id → 404 ───────────────────────────
{
  reset({ status: 404, body: { error: 'export not found' } })
  const res = await eb.auth.getMyExport('nope')
  assert.equal(res.data, null)
  assert.match(res.error, /not found/)
  console.log('✓ getMyExport: 404 returns error, data=null')
}

console.log('\nAll auth-export SDK tests passed.')
