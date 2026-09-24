// Two filters on the same column must both reach the gateway.
// Before 0.7.1, buildParams() keyed filters by column name, so
// .gte('created_at', a).lte('created_at', b) sent only the upper bound.
//
// Run via: npm run build && node test/repeated-filters.mjs

import { createClient } from '../dist/index.js'
import assert from 'node:assert/strict'

let capturedUrl = null
globalThis.fetch = async (url) => {
  capturedUrl = url
  return {
    ok: true,
    status: 200,
    statusText: 'OK',
    headers: { get: (k) => (k.toLowerCase() === 'content-type' ? 'application/json' : null) },
    json: async () => ({ data: [], count: 0 }),
    text: async () => JSON.stringify({ data: [], count: 0 }),
  }
}

const eb = createClient({ url: 'https://example.eurobase.app', apiKey: 'eb_pk_test' })

// Date range on one column + another filter + order/limit.
await eb.db
  .from('events')
  .select('id', 'created_at')
  .gte('created_at', '2026-01-01')
  .lte('created_at', '2026-01-31')
  .eq('status', 'open')
  .order('created_at', { ascending: false })
  .limit(10)

const u = new URL(capturedUrl)
assert.equal(u.pathname, '/v1/db/events')
assert.deepEqual(u.searchParams.getAll('created_at'), ['gte.2026-01-01', 'lte.2026-01-31'],
  'both bounds of the range must be sent')
assert.equal(u.searchParams.get('status'), 'eq.open')
assert.equal(u.searchParams.get('select'), 'id,created_at')
assert.equal(u.searchParams.get('order'), 'created_at.desc')
assert.equal(u.searchParams.get('limit'), '10')

// No filters → no stray "?".
await eb.db.from('events')
assert.equal(new URL(capturedUrl).search, '')

console.log('repeated-filters: ok')
