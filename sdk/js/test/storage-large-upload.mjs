// storage.upload of a file over 45 MB: a signed upload URL for its exact
// size, then a PUT straight to storage with the returned headers. Small
// files keep the multipart path.
//
// Run via: npm run build && node test/storage-large-upload.mjs

import { createClient } from '../dist/index.js'
import assert from 'node:assert/strict'

const calls = []
const json = (obj, status) => new Response(JSON.stringify(obj), { status, headers: { 'content-type': 'application/json' } })
globalThis.fetch = async (url, init = {}) => {
  const u = String(url)
  calls.push({ url: u, method: init.method, headers: init.headers, body: init.body })
  if (u.endsWith('/v1/storage/signed-url')) {
    const req = JSON.parse(init.body)
    if (req.size > 500 * 1024 * 1024) {
      return json({ error: 'file is larger than this plan\'s upload limit of 500 MB' }, 413)
    }
    return json({ url: 'https://s3.example/b/big.bin?sig', expires_at: '2030-01-01T00:00:00Z', headers: { 'x-amz-object-lock-mode': 'COMPLIANCE' } }, 200)
  }
  if (u.startsWith('https://s3.example/')) return new Response('', { status: 200 })
  if (u.endsWith('/v1/storage/upload')) return json({ key: 'small.txt', content_type: 'text/plain', size: 5 }, 201)
  throw new Error('unexpected fetch ' + u)
}

const eb = createClient({ url: 'https://myapp.eurobase.app', apiKey: 'eb_sk_test' })

// Small file: multipart through the API.
let res = await eb.storage.upload('small.txt', new Blob(['hello'], { type: 'text/plain' }))
assert.equal(res.error, null)
assert.ok(calls.at(-1).url.endsWith('/v1/storage/upload'))

// Large file: signed URL with its size, then a PUT to storage.
calls.length = 0
const big = new Blob([new Uint8Array(46 * 1024 * 1024)], { type: 'application/octet-stream' })
res = await eb.storage.upload('big.bin', big)
assert.equal(res.error, null)
assert.equal(res.size, big.size)
assert.equal(calls.length, 2)
assert.deepEqual(JSON.parse(calls[0].body), { key: 'big.bin', operation: 'upload', content_type: 'application/octet-stream', size: big.size })
assert.equal(calls[1].method, 'PUT')
assert.equal(calls[1].headers['x-amz-object-lock-mode'], 'COMPLIANCE', 'signed headers are sent')
assert.equal(calls[1].body, big)

// Over the plan limit: the API's refusal comes back as the error, no PUT.
calls.length = 0
const huge = { size: 501 * 1024 * 1024, type: 'application/octet-stream' }
Object.setPrototypeOf(huge, Blob.prototype)
res = await eb.storage.upload('huge.bin', huge)
assert.match(res.error, /upload limit of 500 MB/)
assert.equal(calls.length, 1)

console.log('storage-large-upload: ok')
