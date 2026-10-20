// db.rpc(): POST /v1/db/rpc/{fn} with named arguments; the result is
// unwrapped from {result}; errors carry the gateway's Postgres code.
//
// Run via: npm run build && node test/rpc.mjs

import { createClient } from '../dist/index.js'
import assert from 'node:assert/strict'

let captured = null
let reply = { status: 200, body: { result: 42 } }
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

// Success: named args in the body, result unwrapped.
let r = await eb.db.rpc('add_points', { user_id: 'u1', points: 3 })
assert.equal(new URL(captured.url).pathname, '/v1/db/rpc/add_points')
assert.equal(captured.init.method, 'POST')
assert.deepEqual(JSON.parse(captured.init.body), { user_id: 'u1', points: 3 })
assert.equal(captured.init.headers.apikey, 'eb_pk_test')
assert.deepEqual(r, { data: 42, error: null, code: null })

// No args → empty object; a null result stays null.
reply = { status: 200, body: { result: null } }
r = await eb.db.rpc('ping')
assert.deepEqual(JSON.parse(captured.init.body), {})
assert.deepEqual(r, { data: null, error: null, code: null })

// The function's own RAISE EXCEPTION.
reply = { status: 400, body: { error: 'not your mission', code: 'P0001' } }
r = await eb.db.rpc('read_mission', { id: 'm1' })
assert.deepEqual(r, { data: null, error: 'not your mission', code: 'P0001', status: 400 })

// Permission / RLS denial.
reply = { status: 403, body: { error: 'permission denied', code: '42501' } }
r = await eb.db.rpc('read_mission', { id: 'm1' })
assert.equal(r.code, '42501')
assert.equal(r.status, 403)

// Unknown function: no code.
reply = { status: 404, body: { error: 'function "nope" not found' } }
r = await eb.db.rpc('nope')
assert.deepEqual(r, { data: null, error: 'function "nope" not found', code: null, status: 404 })

// Name is URL-encoded.
reply = { status: 200, body: { result: 1 } }
await eb.db.rpc('a/b')
assert.equal(new URL(captured.url).pathname, '/v1/db/rpc/a%2Fb')

// Network error: an error result, not a throw.
globalThis.fetch = async () => { throw new Error('offline') }
r = await eb.db.rpc('ping')
assert.deepEqual(r, { data: null, error: 'offline', code: null })

console.log('rpc: ok')
