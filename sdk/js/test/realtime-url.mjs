// Regression test for #62: the realtime WebSocket URL must carry the
// project_id query parameter so the gateway can route events to the
// correct project. The earlier SDK sent only ?token=… and got 400 from
// the post-#62 gateway. The apikey path doesn't strictly require
// project_id (the gateway resolves it from the key), but we include it
// when available so end-user JWT realtime works too.
//
// Run via: npm run build && node test/realtime-url.mjs

import { createClient } from '../dist/index.js'
import assert from 'node:assert/strict'

const API_KEY = 'eb_pk_abc123'
const URL_BASE = 'https://newtek2.eurobase.app'
const PROJECT_ID = '1ac63ce5-1dd0-49a3-bcfe-559fcb4d8506'
const ACCESS_TOKEN = 'eyJ-fake-end-user-jwt'

// ── 1. API key only, no projectId ──────────────────────────────────
{
  const client = createClient({ url: URL_BASE, apiKey: API_KEY })
  const url = client.realtime.buildWebSocketURL()
  const parsed = new URL(url)

  assert.equal(parsed.protocol, 'wss:', `expected wss:// scheme, got ${parsed.protocol}`)
  assert.equal(parsed.pathname, '/v1/realtime')
  assert.equal(parsed.searchParams.get('token'), API_KEY)
  assert.equal(
    parsed.searchParams.get('project_id'),
    null,
    'project_id should be absent when no projectId is configured',
  )
  console.log('✓ apikey-only URL: only token query param')
}

// ── 2. API key + explicit projectId ────────────────────────────────
{
  const client = createClient({ url: URL_BASE, apiKey: API_KEY, projectId: PROJECT_ID })
  const url = client.realtime.buildWebSocketURL()
  const parsed = new URL(url)

  assert.equal(parsed.searchParams.get('token'), API_KEY)
  assert.equal(parsed.searchParams.get('project_id'), PROJECT_ID)
  console.log('✓ apikey + projectId URL: both query params')
}

// ── 3. End-user signed in: access token wins over apikey ───────────
{
  const client = createClient({ url: URL_BASE, apiKey: API_KEY, projectId: PROJECT_ID })
  // Simulate a signed-in user — sets the access token on the shared
  // HttpClient that the realtime client reads.
  client.auth.setSession({
    access_token: ACCESS_TOKEN,
    token_type: 'bearer',
    expires_in: 3600,
    refresh_token: 'rt-fake',
    user: { id: 'u1', email: 'a@b.c', created_at: '', updated_at: '' },
  })

  const url = client.realtime.buildWebSocketURL()
  const parsed = new URL(url)

  assert.equal(
    parsed.searchParams.get('token'),
    ACCESS_TOKEN,
    'end-user access token should take precedence over apikey',
  )
  assert.equal(parsed.searchParams.get('project_id'), PROJECT_ID)
  console.log('✓ signed-in URL: end-user JWT plus projectId')
}

// ── 4. http base URL rewrites to ws, https rewrites to wss ─────────
{
  for (const [base, expectScheme] of [
    ['http://localhost:8080', 'ws:'],
    ['https://api.eurobase.app', 'wss:'],
    ['http://api.eurobase.app/', 'ws:'],
  ]) {
    const client = createClient({ url: base, apiKey: API_KEY })
    const url = client.realtime.buildWebSocketURL()
    const parsed = new URL(url)
    assert.equal(parsed.protocol, expectScheme, `${base} → ${parsed.protocol}, want ${expectScheme}`)
  }
  console.log('✓ scheme rewrite: http/https → ws/wss')
}

// ── 5. Trailing slashes on base URL are trimmed ───────────────────
{
  const client = createClient({ url: 'https://api.eurobase.app///', apiKey: API_KEY })
  const url = client.realtime.buildWebSocketURL()
  assert.equal(
    url.startsWith('wss://api.eurobase.app/v1/realtime?'),
    true,
    `unexpected URL: ${url}`,
  )
  console.log('✓ trailing slashes trimmed')
}

console.log('\nAll realtime URL tests passed.')
