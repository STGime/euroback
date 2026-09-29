# @eurobase/sdk reference for Lovable apps

Written against `@eurobase/sdk` 0.8.1+. All examples assume the shared client and helper from `SKILL.md`: `import { eb, currentUserId } from '@/lib/eurobase'`.

## Return shapes

| Call | Returns |
|---|---|
| `await eb.db.from(t)...` (select) | `{ data: T[] or null, count, error: string or null }` |
| `...single()` | `{ data: T or null, error }`; error `'Row not found'` when empty |
| `insert` / `update` | `{ data: T or null, error }` (the written row) |
| `delete` | `{ error }` |
| `auth.signUp` / `signIn` / `refreshSession` / `signInWithMagicLink` | `{ data: AuthSession or null, error }` |
| `auth.getUser()` | `{ data: AuthUser or null, error }` (asks the server) |
| `auth.getSession()` | `AuthSession or null` (synchronous, from memory) |
| `storage.upload` | `{ key, size, content_type, error }` |
| `storage.createSignedUrl` | `{ url, expires_at, error }` |
| `storage.getPublicUrl` | `string` (public folders only) |
| `storage.list` | `{ objects, has_more, next_cursor, error }` |
| `storage.download` | `Blob` |
| `functions.invoke` | `{ data, error: { status, message } or null }` (`status` is `0` today — show `message`, don't branch on `status`) |

`AuthSession = { access_token, refresh_token, expires_in, token_type, user }`
`AuthUser = { id, email, display_name?, avatar_url?, metadata?, created_at, updated_at }`

Errors are plain strings (functions excepted). Show them with `toast.error(error)`, not `error.message`.

## Database

```typescript
// List
const { data, error } = await eb.db.from('projects')
  .select('id', 'name', 'status')        // omit select() for all columns
  .eq('status', 'active')
  .ilike('name', `%${search}%`)
  .order('created_at', { ascending: false })
  .limit(25)
  .offset(page * 25)

// One row
const { data: project, error } = await eb.db.from('projects').eq('id', id).single()

// Insert (returns the new row)
const { data: created, error } = await eb.db.from('projects')
  .insert({ name, user_id: await currentUserId() })

// Update by id (returns the updated row)
await eb.db.from('projects').update(id, { status: 'archived' })

// Delete by id
await eb.db.from('projects').delete(id)

// IN filter; date range (both bounds apply)
await eb.db.from('tasks').in('project_id', projectIds)
await eb.db.from('events').gte('created_at', from).lte('created_at', to)
```

With TanStack Query, wrap calls so errors throw:

```typescript
export function useProjects() {
  return useQuery({
    queryKey: ['projects'],
    queryFn: async () => {
      const { data, error } = await eb.db.from('projects').order('created_at', { ascending: false })
      if (error) throw new Error(error)
      return (data ?? []) as Project[]
    },
  })
}
```

"Join" pattern (no embeds in the SDK):

```typescript
const { data: tasks } = await eb.db.from('tasks').eq('assignee_id', userId)
const ids = [...new Set((tasks ?? []).map(t => t.project_id))]
const { data: projects } = ids.length
  ? await eb.db.from('projects').in('id', ids)
  : { data: [] }
```

Limits to remember: no upsert, no `.or()`, no `.is(null)`, no nested selects, and `update`/`delete` only by `id`. Several filters on the same column are fine, up to 64 filters per query. Bulk changes across many rows belong in an edge function.

## Auth provider

```typescript
// src/contexts/AuthContext.tsx
import { createContext, useContext, useEffect, useState, type ReactNode } from 'react'
import type { AuthSession } from '@eurobase/sdk'
import { eb } from '@/lib/eurobase'

type Ctx = { session: AuthSession | null; loading: boolean }
const AuthContext = createContext<Ctx>({ session: null, loading: true })

export function AuthProvider({ children }: { children: ReactNode }) {
  const [session, setSession] = useState<AuthSession | null>(null)
  const [loading, setLoading] = useState(true)

  useEffect(() => {              // runs only in the browser
    setSession(eb.auth.getSession())
    setLoading(false)
    return eb.auth.onAuthStateChange((_event, s) => setSession(s))
  }, [])

  return <AuthContext.Provider value={{ session, loading }}>{children}</AuthContext.Provider>
}

export const useAuth = () => useContext(AuthContext)
```

Protected page: while `loading`, render a spinner; if `!session`, navigate to `/login`. Don't decide on the server.

### Server-side rendering (TanStack Start)

Newer Lovable projects render pages on the server, where there is no `localStorage` and so no session. Call `eb.auth.*`, `eb.db.*`, `eb.storage.*` and `eb.realtime.*` only in the browser — `useEffect`, event handlers, client-side TanStack Query hooks — and never in route `loader`s, `beforeLoad` or server functions: they'd see a signed-out user, and a module-level client on the server is shared between visitors. Put auth checks in a client component, not in `beforeLoad`. Older React + Vite projects run entirely in the browser.

Sign in / sign up:

```typescript
const { error } = await eb.auth.signIn({ email, password })
const { error } = await eb.auth.signUp({
  email, password,
  metadata: { display_name: name },
  emailRedirectTo: `${window.location.origin}/verify`,
})
```

If the project requires email confirmation, `signUp` still returns a session (and fires `SIGNED_IN`), but the user must click the email link before they can use the app — call `eb.auth.signOut()` right after `signUp` and show a "check your inbox" screen.

## Auth flows

Each flow needs a page in the app and its full URL in the Eurobase console → project → Auth → **Allowed redirect URLs** (exact match for email flows; OAuth also accepts deeper paths under an allowed URL). The app's origins also go into **Allowed CORS origins**. Remind the user to add both preview origins (`https://<project-id>.lovableproject.com` and `https://id-preview--<project-id>.lovable.app`), the published `https://<name>.lovable.app` URL and its shareable preview `https://preview--<name>.lovable.app`, and any custom domain to both — wildcards like `*.lovable.app` don't work. (Console → project → Connect → Lovable tab adds them.)

Email verification (`/verify`):

```typescript
const token = new URLSearchParams(location.search).get('token')
const { error } = token ? await eb.auth.verifyEmail(token) : { error: 'Missing token' }
// success → "Email confirmed, you can sign in"; offer eb.auth.resendVerification(email) on failure
```

Forgot password (`/forgot-password`) and reset (`/reset-password`):

```typescript
await eb.auth.forgotPassword(email, { emailRedirectTo: `${location.origin}/reset-password` })
// always returns error: null so emails can't be enumerated; always show "if the account exists, we sent a link"

const token = new URLSearchParams(location.search).get('token')!
const { error } = await eb.auth.resetPassword(token, newPassword)
```

Magic link — **only sent to users that already exist**: a magic link signs a user in, it doesn't create one. Sign the user up first (`signUp`). For an unknown address `requestMagicLink` returns no error and sends nothing (so it can't reveal who has an account) — show "check your inbox" either way, and don't treat a missing email as a bug in your code. The same holds for `forgotPassword` and `resendVerification`. The user can see what happened to each request in the Eurobase console → project → Auth → Email log.

```typescript
await eb.auth.requestMagicLink(email, { emailRedirectTo: `${location.origin}/magic` })
// on /magic:
const { data, error } = await eb.auth.signInWithMagicLink(new URLSearchParams(location.search).get('token')!)
```

OAuth (the provider must be enabled in the Eurobase console):

```typescript
eb.auth.signInWithOAuth('google', { redirectTo: `${location.origin}/auth/callback` })

// /auth/callback (client-side):
const { data, error } = eb.auth.handleOAuthCallback()   // reads tokens from the URL hash
if (data) {
  // 0.8.1+: data.user.id / email are set right away; getUser() adds the rest of the profile
  const { data: user } = await eb.auth.getUser()
  navigate('/')
}
```

GDPR "Download my data" button (settings page):

```typescript
const { data: job } = await eb.auth.exportMyData('json')
// poll: const { data } = await eb.auth.getMyExport(job.id); when data.status === 'completed' open data.download_url
```

Rate-limited to one export per user per day by default.

## Storage

```typescript
const userId = await currentUserId()
const path = `${userId}/${crypto.randomUUID()}-${file.name}`

const { key, error } = await eb.storage.upload(path, file, { contentType: file.type })
if (error) throw new Error(error)
await eb.db.from('documents').insert({ user_id: userId, storage_key: key, name: file.name })

// display / download (signed URLs from the app last at most 1 hour)
const { url } = await eb.storage.createSignedUrl(doc.storage_key, 'download', { expiresIn: 3600 })

// list and delete
const { objects } = await eb.storage.list({ prefix: `${userId}/`, limit: 50 })
await eb.storage.remove(doc.storage_key)
```

Store keys, not URLs. For large files, get a signed `upload` URL and `PUT` the file to it directly (the key is reserved for the user before the URL is issued).

Who can see what:

- A user's uploads are private to that user (read, replace, delete). `storage.list()` returns only files the caller may read.
- Developer files in a folder the developer shared (console → Storage → Sharing) are readable by all signed-in users, or publicly. Users can't upload into shared folders.
- Public files: `eb.storage.getPublicUrl('themes/hero.jpg')` gives a plain URL for `<img src>` (public key only; it throws with a secret key).
- To show user A's file to user B (avatars in a list), use an edge function with service access that returns signed URLs for the files B may see.

## Tables and row-level security presets

Tables created with the Eurobase connector (or the console) get row-level security with a preset. Pick deliberately:

| Preset | Who can read | Who can write | Use for |
|---|---|---|---|
| `owner_access` | only the owner | only the owner | private per-user data (default with a `user_id`, `owner_id` or `created_by` column) |
| `authenticated_read_owner_write` | signed-in users | only the owner | team or community data |
| `public_read_owner_write` | everyone | only the owner | public profiles, posts |
| `read_only` | everyone | nobody from the app | reference data you seed |
| `service_only` | nobody from the app | nobody from the app | data only edge functions touch |
| `full_access` | everyone | everyone | only when the user explicitly wants an anonymous public table |

Raw SQL policies: every insert runs `INSERT ... RETURNING *`, and Postgres checks the SELECT policy when reading the row back, so pair every INSERT policy with a SELECT policy.

## Realtime

```typescript
useEffect(() => {
  const sub = eb.realtime.on('messages', 'INSERT', (event) => {
    setMessages(m => [...m, event.record])
  })
  return () => eb.realtime.off(sub)
}, [])
```

The client needs `projectId` (see `SKILL.md` setup) — without it, signed-in users can't connect. Events carry `type`, `record`, `old_record`. The server filters by owner column (`user_id`, `owner_id`, `created_by`, `uploaded_by`): signed-in users receive only their own rows on tables that have one, and anonymous visitors receive nothing from those tables. Custom RLS policies are **not** enforced over realtime yet, so don't subscribe to tables whose privacy depends on custom policies.

## Edge functions

Use for anything that needs secrets, elevated access, or to run without a browser: payments (e.g. Mollie), webhooks, sending email, calling third-party APIs, bulk updates, scheduled jobs.

```typescript
// Deploy in the Eurobase console → Functions (or with the Eurobase CLI)
import type { EdgeHandler } from '@eurobase/sdk/functions'

const handler: EdgeHandler = async (req, ctx) => {
  if (!ctx.user) return new Response('Unauthorized', { status: 401 })

  const { projectId } = (await req.json()) as { projectId: string }
  const rows = await ctx.db.sql<{ id: string; name: string }>(
    'SELECT id, name FROM projects WHERE id = $1 AND user_id = $2',
    [projectId, ctx.user.id],
  )
  if (!rows[0]) return new Response('Not found', { status: 404 })

  const apiKey = await ctx.vault.get('mollie_api_key')     // set in console → Vault
  ctx.log.info('creating payment', { projectId })
  // ... call the third-party API with apiKey ...
  return Response.json({ ok: true })
}
export default handler
```

`ctx.user` is set when the caller is signed in and the function has **Require JWT** on (the default); a function with it off also accepts signed-out callers, and `ctx.user` is then `null`. Treat `ctx.db.sql` as privileged: always scope queries with `ctx.user.id` yourself and use `$1` parameters, never string concatenation. `ctx` also has `storage.upload / createSignedUrl / delete`, `env`, and `requestId`.

Calling it from the app:

```typescript
const { data, error } = await eb.functions.invoke<{ ok: boolean }>('create-payment', { body: { projectId } })
if (error) toast.error(error.message)
```

The signed-in user's token is attached automatically, which is what populates `ctx.user`.

Try a deployed function in the console → Functions → select it → **Test**: method, body, optional "run as end user"; it shows the status, response, `ctx.log` lines and the equivalent curl command.

Schedules (cron) are created in the console → Cron & RPC, not from the browser.

## Common errors

| Symptom | Cause | Fix |
|---|---|---|
| Every Eurobase call fails in the browser with a CORS error; nothing loads | the app's origin isn't in Allowed CORS origins | add the preview, published and custom-domain origins in console → project → Auth → Allowed CORS origins (or Connect → Lovable) |
| "row-level security policy denied this operation" on insert, even with `WITH CHECK (true)` | no SELECT policy for the `RETURNING *` read-back | add a matching SELECT policy, or create the table with a preset |
| Works in the Eurobase table editor, fails in the app | table editor bypasses RLS | fix the policies; the editor proves nothing |
| Insert fails on a `user_id` table | `user_id` not set, or set to another user | set `user_id: session.user.id` |
| Page shows logged-out state on first load, then flips | code ran during server rendering | move the call into `useEffect` / a client-only query |
| Verification or OAuth link errors / lands on the wrong domain | URL not in Allowed redirect URLs | add the page URLs in console → project → Auth → Allowed redirect URLs |
| Upload refused / key already exists | the key belongs to another user, or is in a shared folder | prefix keys with the user's id; users can't write into shared folders |
| Other users' avatars don't load | users can only read their own files | serve them through an edge function (signed URLs) or a developer-managed public folder |
| `update`/`delete` "not found" | table has no `id` primary key, or row belongs to another user | add `id uuid primary key`, check ownership |
| 401 from an edge function | called while signed out, or the function requires a signed-in user | check `getSession()` first |
