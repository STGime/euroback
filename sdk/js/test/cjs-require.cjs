// A CommonJS backend can require() the SDK (0.13.1). Loads the package by
// its own name, so the "exports" map in package.json is what's tested.
const assert = require('node:assert/strict')
const sdk = require('@eurobase/sdk')

assert.equal(typeof sdk.createClient, 'function', 'createClient is exported')
assert.equal(typeof sdk.ADMIN_NEEDS_SECRET_KEY, 'string', 'admin exports are there')
assert.equal(typeof sdk.ACCOUNT_SUSPENDED, 'string', 'auth exports are there')

const eb = sdk.createClient({ url: 'https://example.eurobase.app', apiKey: 'eb_pk_test', auth: { persistSession: false } })
assert.equal(typeof eb.withAccessToken, 'function', 'server-side API is there')
assert.equal(typeof eb.auth.verifyAccessToken, 'function')
assert.equal(typeof eb.auth.admin.listUsers, 'function')
const user = eb.withAccessToken('header.payload.signature')
assert.equal(typeof user.db.from, 'function')

console.log('cjs-require: ok')
