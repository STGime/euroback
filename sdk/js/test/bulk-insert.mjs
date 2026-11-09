// db.from(t).insert(): one object → that row; an array → all rows in one
// request (POST /v1/db/{table} with an array body), data = the rows.
//
// Run via: npm run build && node test/bulk-insert.mjs

import { createClient } from '../dist/index.js'
import assert from 'node:assert/strict'

let captured = null
let reply = { status: 201, body: {} }
globalThis.fetch = async (url, init) => {
  captured = { url, init }
  const { status, body } = reply
  return {
    ok: status >= 200 && status < 300,
    status,
    statusText: String(status),
    headers: { get: (k) => (k.toLowerCase() === 'content-type' ? 'application/json' : null) },
    json: async () => body,
    text: async () => JSON.stringify(body),
  }
}

const eb = createClient({ url: 'https://example.eurobase.app', apiKey: 'eb_pk_test' })

// Single object: unchanged behaviour.
reply = { status: 201, body: { id: '1', name: 'a' } }
let r = await eb.db.from('jobs').insert({ name: 'a' })
assert.equal(new URL(captured.url).pathname, '/v1/db/jobs')
assert.equal(captured.init.method, 'POST')
assert.deepEqual(JSON.parse(captured.init.body), { name: 'a' })
assert.deepEqual(r, { data: { id: '1', name: 'a' }, count: null, error: null })

// Array: sent as an array, rows returned with a count.
reply = { status: 201, body: [{ id: '1', name: 'a' }, { id: '2', name: 'b' }] }
r = await eb.db.from('jobs').insert([{ name: 'a' }, { name: 'b' }])
assert.deepEqual(JSON.parse(captured.init.body), [{ name: 'a' }, { name: 'b' }])
assert.deepEqual(r, { data: [{ id: '1', name: 'a' }, { id: '2', name: 'b' }], count: 2, error: null })

// Error: nothing inserted, error passed through.
reply = { status: 400, body: { error: 'insert between 1 and 1000 rows per request' } }
r = await eb.db.from('jobs').insert([])
assert.equal(r.data, null)
assert.equal(r.error, 'insert between 1 and 1000 rows per request')

console.log('bulk-insert: ok')
