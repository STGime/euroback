// Closes #112. Asserts the SDK builds the right URL + payload shape for
// eb.functions.schedules.{create,update,get,delete,list,createOrUpdate}.
// Uses a stubbed global fetch so the test runs without a real gateway.
//
// Run via: npm run build && node test/schedules.mjs

import { createClient } from '../dist/index.js'
import assert from 'node:assert/strict'

const API_KEY = 'eb_sk_secret_test'
const URL_BASE = 'https://newtek2.eurobase.app'

let captured = null
let nextResponse = null

function reset(response) {
  captured = null
  nextResponse = response
}

// Stub global fetch. The SDK uses native fetch in http.ts.
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
    headers: {
      get: (k) => headers[k.toLowerCase()] ?? null,
    },
    async json() { return body },
  }
}

const eb = createClient({ url: URL_BASE, apiKey: API_KEY })

const sampleServerRow = {
  id: 'sched-1',
  project_id: 'proj-1',
  name: 'purge-expired-images',
  schedule: '0 4 * * *',
  timezone: 'UTC',
  action_type: 'function',
  action: 'purge-expired-images',
  description: 'daily purge',
  payload: { mode: 'soft' },
  headers: { 'X-Internal': '1' },
  enabled: true,
  last_run_at: null,
  last_error: null,
  run_count: 0,
  created_at: '2026-05-08T00:00:00Z',
  updated_at: '2026-05-08T00:00:00Z',
}

// ── 1. create() — POST /v1/schedules with the right body shape ─────
{
  reset({ status: 201, body: sampleServerRow })
  const res = await eb.functions.schedules.create('purge-expired-images', {
    functionName: 'purge-expired-images',
    cron: '0 4 * * *',
    timezone: 'UTC',
    description: 'daily purge',
    payload: { mode: 'soft' },
    headers: { 'X-Internal': '1' },
  })
  assert.equal(captured.method, 'POST')
  assert.equal(captured.url, `${URL_BASE}/v1/schedules`)
  assert.deepEqual(captured.body, {
    name: 'purge-expired-images',
    schedule: '0 4 * * *',
    timezone: 'UTC',
    action_type: 'function',
    action: 'purge-expired-images',
    description: 'daily purge',
    payload: { mode: 'soft' },
    headers: { 'X-Internal': '1' },
  })
  assert.equal(res.error, null)
  assert.equal(res.data.functionName, 'purge-expired-images')
  assert.equal(res.data.cron, '0 4 * * *')
  assert.equal(res.data.projectId, 'proj-1')
  console.log('✓ create: posts to /v1/schedules with action_type=function')
}

// ── 2. create() collision surfaces code=already_exists ────────────
{
  reset({ status: 409, body: { error: 'schedule with this name already exists' } })
  const res = await eb.functions.schedules.create('purge-expired-images', {
    functionName: 'purge-expired-images',
    cron: '0 4 * * *',
  })
  assert.equal(res.data, null)
  assert.equal(res.error.code, 'already_exists')
  console.log('✓ create: 409 → error.code=already_exists')
}

// ── 3. update() — PATCH /v1/schedules/:name with partial body ─────
{
  reset({ status: 200, body: { ...sampleServerRow, enabled: false } })
  const res = await eb.functions.schedules.update('purge-expired-images', {
    enabled: false,
    cron: '0 5 * * *',
  })
  assert.equal(captured.method, 'PATCH')
  assert.equal(captured.url, `${URL_BASE}/v1/schedules/purge-expired-images`)
  assert.deepEqual(captured.body, { schedule: '0 5 * * *', enabled: false })
  assert.equal(res.error, null)
  assert.equal(res.data.enabled, false)
  console.log('✓ update: maps cron→schedule, sends only changed fields')
}

// ── 4. get() — GET /v1/schedules/:name ────────────────────────────
{
  reset({ status: 200, body: sampleServerRow })
  const res = await eb.functions.schedules.get('purge-expired-images')
  assert.equal(captured.method, 'GET')
  assert.equal(captured.url, `${URL_BASE}/v1/schedules/purge-expired-images`)
  assert.equal(res.data.name, 'purge-expired-images')
  console.log('✓ get: GET /v1/schedules/:name')
}

// ── 5. list() — GET /v1/schedules, returns array ──────────────────
{
  reset({ status: 200, body: [sampleServerRow] })
  const res = await eb.functions.schedules.list()
  assert.equal(captured.method, 'GET')
  assert.equal(captured.url, `${URL_BASE}/v1/schedules`)
  assert.equal(res.error, null)
  assert.equal(res.data.length, 1)
  assert.equal(res.data[0].functionName, 'purge-expired-images')
  console.log('✓ list: GET /v1/schedules → ScheduleRow[]')
}

// ── 6. delete() — DELETE /v1/schedules/:name ──────────────────────
{
  reset({ status: 204, body: {}, headers: {} })
  const res = await eb.functions.schedules.delete('purge-expired-images')
  assert.equal(captured.method, 'DELETE')
  assert.equal(captured.url, `${URL_BASE}/v1/schedules/purge-expired-images`)
  assert.equal(res.error, null)
  console.log('✓ delete: DELETE /v1/schedules/:name')
}

// ── 7. URL encoding: schedule name with special chars is encoded ──
{
  reset({ status: 200, body: sampleServerRow })
  await eb.functions.schedules.get('weird name/with/slashes')
  assert.equal(
    captured.url,
    `${URL_BASE}/v1/schedules/${encodeURIComponent('weird name/with/slashes')}`,
  )
  console.log('✓ get: schedule name is URL-encoded')
}

// ── 8. createOrUpdate(): create succeeds first time, updates on conflict ──
{
  // First call: 409 → triggers update.
  let calls = 0
  globalThis.fetch = async (url, init) => {
    calls++
    captured = { url, method: init?.method ?? 'GET', body: init?.body && JSON.parse(init.body) }
    if (calls === 1) {
      return {
        ok: false, status: 409, statusText: 'HTTP 409',
        headers: { get: (k) => k.toLowerCase() === 'content-type' ? 'application/json' : null },
        async json() { return { error: 'schedule with this name already exists' } },
      }
    }
    return {
      ok: true, status: 200, statusText: 'HTTP 200',
      headers: { get: (k) => k.toLowerCase() === 'content-type' ? 'application/json' : null },
      async json() { return sampleServerRow },
    }
  }
  const res = await eb.functions.schedules.createOrUpdate('purge-expired-images', {
    functionName: 'purge-expired-images',
    cron: '0 4 * * *',
  })
  assert.equal(calls, 2, 'expected two calls: create then update')
  assert.equal(captured.method, 'PATCH')
  assert.equal(res.error, null)
  console.log('✓ createOrUpdate: 409 on create falls through to update')
}

console.log('\nAll schedules SDK tests passed.')
