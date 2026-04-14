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

// Magic link
await eb.auth.requestMagicLink('user@example.com')

// Get current user
const { data: user } = await eb.auth.getUser()

// Sign out
await eb.auth.signOut()
```

## Database

```ts
// Select with filters
const { data, error } = await eb.db
  .from('todos')
  .select('id', 'title', 'completed')
  .eq('completed', 'false')
  .order('created_at', { ascending: false })
  .limit(20)

// Insert
const { data, error } = await eb.db
  .from('todos')
  .insert({ title: 'Ship it', completed: false })

// Update
const { data, error } = await eb.db
  .from('todos')
  .update({ completed: true })
  .eq('id', 'some-uuid')

// Delete
const { data, error } = await eb.db
  .from('todos')
  .delete()
  .eq('id', 'some-uuid')
```

## Storage

```ts
// Upload a file
const { data, error } = await eb.storage
  .from('avatars')
  .upload('profile.png', file, { contentType: 'image/png' })

// Get a signed URL
const { data } = await eb.storage
  .from('avatars')
  .createSignedUrl('profile.png', { expiresIn: 3600 })

// List files
const { data } = await eb.storage
  .from('documents')
  .list({ prefix: 'invoices/' })
```

## Realtime

```ts
// Subscribe to changes
const subscription = eb.realtime.subscribe('orders', (event) => {
  console.log(event.type, event.row) // INSERT, UPDATE, or DELETE
})

// Unsubscribe
subscription.unsubscribe()
```

## Edge Functions

```ts
// Invoke a function
const { data, error } = await eb.functions.invoke('send-welcome-email', {
  body: { userId: '123' },
})
```

## Vault

```ts
// Store a secret (requires secret API key)
await eb.vault.set('stripe_key', 'sk_live_...')

// Retrieve a secret
const { data } = await eb.vault.get('stripe_key')

// List secrets (metadata only, no values)
const { data } = await eb.vault.list()
```

## Connectivity Check

```ts
const { ok, latency_ms } = await eb.status()
```

## EU Sovereignty

All data processed through the Eurobase SDK stays on EU infrastructure (Scaleway, France). No US CLOUD Act exposure. GDPR-compliant by design.

## License

MIT
