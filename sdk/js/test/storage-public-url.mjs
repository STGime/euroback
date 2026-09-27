// storage.getPublicUrl (0.8.0, #697): a plain URL for a file in a public
// shared folder, carrying the public key; refuses a secret key.
//
// Run via: npm run build && node test/storage-public-url.mjs

import { createClient } from '../dist/index.js'
import assert from 'node:assert/strict'

const eb = createClient({ url: 'https://myapp.eurobase.app/', apiKey: 'eb_pk_test123' })
assert.equal(
  eb.storage.getPublicUrl('themes/nordic/hero image.jpg'),
  'https://myapp.eurobase.app/v1/storage/themes/nordic/hero%20image.jpg?apikey=eb_pk_test123',
  'slashes kept, segments encoded, trailing slash on the base URL dropped',
)
assert.equal(
  eb.storage.getPublicUrl('icons/ä#?.png'),
  'https://myapp.eurobase.app/v1/storage/icons/%C3%A4%23%3F.png?apikey=eb_pk_test123',
)

const server = createClient({ url: 'https://myapp.eurobase.app', apiKey: 'eb_sk_secret' })
assert.throws(() => server.storage.getPublicUrl('themes/a.png'), /public key/,
  'a secret key must never be put in a URL')

console.log('storage-public-url: ok')
