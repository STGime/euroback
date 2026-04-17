# @eurobase/sdk

The official TypeScript SDK for [Eurobase](https://eurobase.app) — the EU-sovereign Backend-as-a-Service platform.

Zero external dependencies. Works in browsers and Node.js 18+.

## Install

```bash
npm install @eurobase/sdk
```

## Quick Start

```ts
import { createClient } from '@eurobase/sdk'

const eb = createClient({
  url: 'https://my-app.eurobase.app',
  apiKey: 'eb_pk_...',
})
```

Or use a connection string:

```ts
const eb = createClient('eurobase://eb_pk_xxx@my-app.eurobase.app')
```

## Authentication

```ts
// Sign up
const { data, error } = await eb.auth.signUp({
  email: 'user@example.com',
  password: 'securepassword',
})

// Sign in
const { data, error } = await eb.auth.signIn({
  email: 'user@example.com',
  password: 'securepassword',
})

// OAuth (redirects the browser)
eb.auth.signInWithOAuth('google', {
  redirectTo: 'https://myapp.com/callback',
})

// Handle OAuth callback (on your callback page)
const { data: session, error } = eb.auth.handleOAuthCallback()

// Magic link
await eb.auth.requestMagicLink('user@example.com')
await eb.auth.signInWithMagicLink(token)

// Get current user (from server)
const { data: user } = await eb.auth.getUser()

// Get current session (from memory)
const session = eb.auth.getSession()

// Refresh session
await eb.auth.refreshSession()

// Listen for auth state changes
const unsubscribe = eb.auth.onAuthStateChange((event, session) => {
  console.log(event) // SIGNED_IN | SIGNED_OUT | TOKEN_REFRESHED
})

// Sign out
await eb.auth.signOut()
```

Sessions are automatically persisted to `localStorage` (in browsers) and refreshed before expiry.

## Database

```ts
// Select with filters
const { data, error } = await eb.db
  .from('todos')
  .select('id', 'title', 'completed')
  .eq('completed', 'false')
  .order('created_at', { ascending: false })
  .limit(20)

// Get a single row
const { data, error } = await eb.db
  .from('todos')
  .eq('id', 'some-uuid')
  .single()

// Insert
const { data, error } = await eb.db
  .from('todos')
  .insert({ title: 'Ship it', completed: false })

// Update by ID
const { data, error } = await eb.db
  .from('todos')
  .update('some-uuid', { completed: true })

// Delete by ID
const { error } = await eb.db
  .from('todos')
  .delete('some-uuid')
```

### Available filters

| Method | SQL equivalent |
|--------|---------------|
| `.eq(col, val)` | `col = val` |
| `.neq(col, val)` | `col != val` |
| `.gt(col, val)` | `col > val` |
| `.gte(col, val)` | `col >= val` |
| `.lt(col, val)` | `col < val` |
| `.lte(col, val)` | `col <= val` |
| `.like(col, pattern)` | `col LIKE pattern` |
| `.ilike(col, pattern)` | `col ILIKE pattern` (case-insensitive) |
| `.in(col, [a, b])` | `col IN (a, b)` |

Pagination: `.limit(n)` and `.offset(n)`.

## Storage

```ts
// Upload a file
const { key, size, error } = await eb.storage.upload(
  'avatars/profile.png',
  file,
  { contentType: 'image/png' },
)

// Download a file
const blob = await eb.storage.download('avatars/profile.png')

// List files
const { objects, has_more, error } = await eb.storage.list({
  prefix: 'avatars/',
  limit: 50,
})

// Generate a signed download URL (temporary, no auth needed)
const { url, expires_at } = await eb.storage.createSignedUrl(
  'avatars/profile.png',
  'download',
  { expiresIn: 3600 },
)

// Generate a signed upload URL
const { url } = await eb.storage.createSignedUrl(
  'uploads/doc.pdf',
  'upload',
  { expiresIn: 600 },
)

// Delete a file
const { error } = await eb.storage.remove('avatars/profile.png')
```

Supports `File`, `Blob`, `ArrayBuffer`, `Uint8Array`, and Node.js `Buffer` for uploads.

## Realtime

```ts
// Subscribe to all changes on a table
const sub = eb.realtime.on('orders', '*', (event) => {
  console.log(event.type, event.record) // INSERT, UPDATE, or DELETE
})

// Subscribe to specific events
const sub = eb.realtime.on('orders', 'INSERT', (event) => {
  console.log('New order:', event.record)
})

// Unsubscribe
eb.realtime.off(sub)

// Disconnect (closes WebSocket, clears all subscriptions)
eb.realtime.disconnect()
```

Reconnects automatically with exponential backoff (1s, 2s, 4s, ... up to 30s).

## Edge Functions

```ts
// Invoke a function (POST by default)
const { data, error } = await eb.functions.invoke('send-welcome-email', {
  body: { userId: '123' },
})

// Use a different HTTP method
const { data } = await eb.functions.invoke('get-stats', {
  method: 'GET',
})

// Custom headers
const { data } = await eb.functions.invoke('webhook-proxy', {
  body: payload,
  headers: { 'X-Custom': 'value' },
})
```

## Vault

```ts
// Store a secret (requires secret API key — eb_sk_*)
await eb.vault.set('stripe_key', 'sk_live_...', 'Stripe production key')

// Retrieve a secret
const { data } = await eb.vault.get('stripe_key')

// List secrets (metadata only, no values)
const { data } = await eb.vault.list()

// Delete a secret
await eb.vault.delete('stripe_key')
```

The vault uses AES-256-GCM encryption at rest. Only accessible with the secret API key (`eb_sk_*`), never the public key.

## Connectivity Check

```ts
const { ok, latency_ms } = await eb.status()
```

## TypeScript

All types are exported:

```ts
import type {
  EurobaseClient,
  EurobaseConfig,
  QueryResult,
  AuthUser,
  AuthSession,
  RealtimeEvent,
  ObjectInfo,
  VaultSecret,
  FunctionInvokeOptions,
} from '@eurobase/sdk'
```

## EU Sovereignty

All data processed through the Eurobase SDK stays on EU infrastructure (Scaleway, France). No US CLOUD Act exposure. GDPR-compliant by design.

## License

MIT
