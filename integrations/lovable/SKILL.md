---
name: eurobase
description: Use when the project uses Eurobase (eurobase.app, @eurobase/sdk) as its backend, or when the user asks for an EU-hosted, GDPR-compliant or "not Supabase" backend. Covers setup, database, auth, storage, realtime, row-level security and edge functions with @eurobase/sdk. Load it for ANY backend work in a Eurobase project (login, sign-up, saving data, tables, file uploads, user accounts), even if the user doesn't say "Eurobase" again. Not for projects that use Lovable Cloud or Supabase.
---

# Building a Lovable app on Eurobase

Eurobase is an EU-sovereign backend (Postgres, auth, storage, realtime, edge functions, vault) hosted in France. This project uses Eurobase **instead of** Lovable Cloud and Supabase. The user chose it for data sovereignty, so the worst mistake is silently moving their data elsewhere.

## Rule 1: Never enable Lovable Cloud or Supabase

Don't enable Lovable Cloud, connect Supabase, create `src/integrations/supabase`, install `@supabase/supabase-js`, or write Lovable Cloud functions. Build every backend feature (login, data, uploads, emails, jobs, server logic) on Eurobase. If something is impossible on Eurobase, stop and tell the user.

## Rule 2: Only public values go in the app

`.env` (Lovable commits it; fine for these public values):

```
VITE_EUROBASE_URL=https://my-app.eurobase.app
VITE_EUROBASE_PROJECT_ID=<project uuid>
VITE_EUROBASE_PUBLIC_KEY=eb_pk_...
```

If any is missing, ask the user: the Eurobase console → project → **Connect** → **Lovable** has these lines. **Never** put a secret key (`eb_sk_...`) anywhere in the project. If the user pastes one, don't use it; tell them to rotate it (console → Settings). Anything needing it belongs in a Eurobase edge function.

## Setup

Install `@eurobase/sdk` **0.8.1+**, create one shared client and import it everywhere:

```typescript
// src/lib/eurobase.ts
import { createClient } from '@eurobase/sdk'

export const eb = createClient({
  url: import.meta.env.VITE_EUROBASE_URL,
  apiKey: import.meta.env.VITE_EUROBASE_PUBLIC_KEY,
  projectId: import.meta.env.VITE_EUROBASE_PROJECT_ID, // required for realtime with signed-in users
})

// The signed-in user's id. SDK 0.8.1+ fills it right after an OAuth
// redirect; the server fallback keeps older installs (0.8.0) working.
export async function currentUserId(): Promise<string | null> {
  const id = eb.auth.getSession()?.user.id
  if (id) return id
  const { data } = await eb.auth.getUser()
  return data?.id ?? null
}
```

The client keeps the session (in `localStorage`) and sends the user's token with every call.

## Tell the user: allow the app's URLs

When you first wire up Eurobase, and whenever auth changes, tell the user: Eurobase console → project → **Auth** → add the preview origins (inside the editor the preview runs on `https://<project-id>.lovableproject.com`, in its own tab on `https://id-preview--<project-id>.lovable.app` — both are needed), the published `https://<name>.lovable.app` URL and its shareable preview `https://preview--<name>.lovable.app`, and any custom domain to **Allowed CORS origins** (without it every call fails with a CORS error) and to **Allowed redirect URLs** with the sign-in pages (`/verify`, `/reset-password`, `/magic`, `/auth/callback`). Wildcards like `*.lovable.app` don't work. Shortcut: console → project → **Connect** → **Lovable**: paste the editor URL (`lovable.dev/projects/…`) as the preview URL and the published URL — it adds all four origins.

## Server-side rendering

On TanStack Start projects, call `eb.*` only in the browser (`useEffect`, event handlers, client-side queries) — never in `loader`, `beforeLoad` or server functions (no session there, and the client would be shared between visitors). Protect pages client-side: show a loading state until the session is known.

## This is NOT supabase-js

| Task | supabase-js habit (wrong here) | Eurobase |
|---|---|---|
| Pick columns | `.select('id, title')` | `.select('id', 'title')` (separate args) |
| Update | `.update({...}).eq('id', x)` | `.update(x, {...})` (by `id`) |
| Delete | `.delete().eq('id', x)` | `.delete(x)` (by `id`) |
| Password sign-in | `signInWithPassword(...)` | `signIn({ email, password })` |
| Current session | `await getSession()` → `data.session` | `getSession()` is **sync**, session or `null` |
| Errors | `error.message` | `error` is a **string** (functions: `{ message }`) |
| Insert returning row | `.insert(...).select().single()` | `.insert({...})` returns the row |
| Upsert, `.or()`, `.is()`, joins | exist | **not available**: see below |

Always check `error` before `data`. Full reference: `references/sdk-reference.md` — read it before writing auth pages, storage, realtime or edge functions.

## Database

```typescript
const { data, error } = await eb.db.from('todos')
  .select('id', 'title', 'done').eq('done', false)
  .order('created_at', { ascending: false }).limit(20)
const { data: todo } = await eb.db.from('todos').eq('id', id).single()
await eb.db.from('todos').insert({ title, user_id: await currentUserId() })
await eb.db.from('todos').update(id, { done: true })
await eb.db.from('todos').delete(id)
```

Filters: `eq, neq, gt, gte, lt, lte, like, ilike, in` (date ranges with `gte` + `lte` work). Paging: `limit, offset`. No joins (run two queries, `.in('id', ids)`), no upsert (query, then insert or update), no OR / null checks (filter client-side, or a view / edge function).

## Tables and security

Every table needs `id uuid primary key default gen_random_uuid()` (update/delete use `id`) and usually `created_at timestamptz default now()`; per-user data gets `user_id uuid`, set from `currentUserId()` on insert.

Use the Eurobase MCP connector if connected: inspect the schema first, create tables with its create-table tool. Row-level security is switched on with a preset (`owner_access` by default for tables with `user_id`); presets are listed in the reference. Avoid `full_access` unless the user asks for an anonymous public table. Without the connector, give the user the SQL for the console's SQL editor; don't pretend the table exists.

Raw SQL policies: pair every INSERT policy with a SELECT policy (inserts run `RETURNING *`). The console's table editor bypasses RLS, so "works in the console" proves nothing.

## Auth

```typescript
await eb.auth.signUp({ email, password, emailRedirectTo: `${location.origin}/verify` })
await eb.auth.signIn({ email, password })
await eb.auth.signOut()
const session = eb.auth.getSession()                     // sync, null when signed out
const unsubscribe = eb.auth.onAuthStateChange((event, s) => { /* SIGNED_IN | SIGNED_OUT | TOKEN_REFRESHED */ })
```

Build an `AuthProvider` (client-side, see the reference). Verification, password reset, magic links and OAuth need pages at `/verify`, `/reset-password`, `/magic`, `/auth/callback` (reference: "Auth flows"). If the project requires email confirmation, sign the user out after `signUp` and show "check your inbox".

## Storage

Users own their uploads: only they can read, replace or delete them. Prefix keys with the user id, store the **key** in the database, and show files with signed URLs (≤ 1 hour):

```typescript
const userId = await currentUserId()
const { key, error } = await eb.storage.upload(`${userId}/avatar.png`, file, { contentType: file.type })
const { url } = await eb.storage.createSignedUrl(key, 'download', { expiresIn: 3600 })
```

App assets everyone sees: the developer uploads them in the console into a folder shared as **Public**; show them with `eb.storage.getPublicUrl('themes/hero.jpg')`. One user's files shown to others (avatars in a list): an edge function that returns signed URLs. See the reference.

## Edge functions (server-side logic)

Payments, webhooks, emails, third-party API keys and anything needing the secret key run as Eurobase edge functions, never in the browser or on Lovable Cloud. Call them with `eb.functions.invoke('name', { body })`. Write the code (typed with `EdgeHandler` from `@eurobase/sdk/functions`) and tell the user to deploy it in the console (Functions), try it with **Functions → Test**, and put third-party secrets in console → Vault (`ctx.vault.get(...)`). Template in the reference.

## Before calling a backend feature done

- No `supabase` imports, no Lovable Cloud tables or functions, no `eb_sk_` in the repo.
- Every table exists in Eurobase with RLS on and the right preset.
- Signed-out users can't reach private pages; a second test account can't see the first one's rows.
- The user was told to allow the app's URLs (CORS origins + redirect URLs).
