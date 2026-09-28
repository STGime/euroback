---
name: eurobase
description: Use when the project uses Eurobase (eurobase.app, @eurobase/sdk) as its backend, or when the user asks for an EU-hosted, GDPR-compliant or "not Supabase" backend. Covers setup, database, auth, storage, realtime, row-level security and edge functions with @eurobase/sdk. Load it for ANY backend work in a Eurobase project (login, sign-up, saving data, tables, file uploads, user accounts), even if the user doesn't say "Eurobase" again. Not for projects that use Lovable Cloud or Supabase.
---

# Building a Lovable app on Eurobase

Eurobase is an EU-sovereign Backend-as-a-Service (Postgres, auth, storage, realtime, edge functions, vault) hosted on Scaleway in France. This project uses Eurobase **instead of** Lovable Cloud and Supabase. The user chose it for data sovereignty, so the most damaging mistake is silently moving their data somewhere else.

## Rule 1: Never enable Lovable Cloud or Supabase

Do not enable Lovable Cloud, connect Supabase, create `src/integrations/supabase`, install `@supabase/supabase-js`, or write Lovable Cloud edge functions in this project. When a feature needs a backend (login, saving data, uploads, emails, scheduled jobs, server-side logic), build it on Eurobase. If something is genuinely impossible on Eurobase, stop and tell the user rather than reaching for Cloud.

## Rule 2: Only the public key goes in the app

- `VITE_EUROBASE_URL`: the project URL, e.g. `https://my-app.eurobase.app`
- `VITE_EUROBASE_PUBLIC_KEY`: starts with `eb_pk_`, safe to ship to the browser; row-level security protects the data

Put both in `.env` (Lovable commits `.env`; that is fine for these two public values). If either is missing, ask the user for them: the console's project **Connect** page → **Lovable** tab has the `.env` lines.

**Never** put a secret key (`eb_sk_...`) in the project: not in `.env`, not in code, not in Lovable Secrets. If the user pastes one into chat, don't use it, tell them it grants full access and bypasses row-level security, and recommend rotating it under Project Settings in the Eurobase console. Anything that needs the secret key belongs in a Eurobase edge function (see below).

## Setup

Install `@eurobase/sdk` **0.8.0 or later** and create one shared client:

```typescript
// src/lib/eurobase.ts
import { createClient } from '@eurobase/sdk'

export const eb = createClient({
  url: import.meta.env.VITE_EUROBASE_URL,
  apiKey: import.meta.env.VITE_EUROBASE_PUBLIC_KEY,
})
```

Import `eb` everywhere; never create a second client. The client keeps the signed-in user's token and attaches it to every db, storage, functions and realtime call automatically, and persists the session in `localStorage`.

Quick connectivity check while debugging: `await eb.status()` → `{ ok, latency_ms }`.

## The app's URLs must be allowed (tell the user)

Before the first backend call works, and again whenever auth changes, tell the user: in the Eurobase console → the project → **Auth**, add the app's URLs — the Lovable preview URL, the published `*.lovable.app` URL and any custom domain — to **both**:

- **Allowed CORS origins** (exact origins like `https://my-app.lovable.app`, no paths): without it the browser blocks every Eurobase call from that URL. The symptom is a CORS error in the browser console and nothing loading.
- **Allowed redirect URLs** (the origin, plus the full URLs of the verify / reset / magic-link pages): without it verification, password reset, magic links and OAuth fail.

Wildcards like `*.lovable.app` are not accepted — list each URL. The console's project **Connect** page → **Lovable** tab adds them to both lists in one step.

## Server-side rendering (TanStack Start projects)

Newer Lovable projects render pages on the server. On the server there is no `localStorage`, so there is no session. Therefore:

- Call `eb.auth.*`, `eb.db.*`, `eb.storage.*` and `eb.realtime.*` **only in the browser**: inside `useEffect`, event handlers, or TanStack Query hooks that run client-side.
- Do **not** call Eurobase from route `loader`s, `beforeLoad`, or server functions. They run on the server, see a logged-out user, and a module-level client on the server is shared between visitors.
- Protect pages in a client component: render a loading state until the session is known, then redirect signed-out users. Don't put the auth check in `beforeLoad`.

Older React + Vite projects run entirely in the browser, so this section doesn't restrict them.

## This is NOT supabase-js

The API looks similar but differs in exactly the places you will be tempted to guess. Use these forms:

| Task | supabase-js habit (wrong here) | Eurobase |
|---|---|---|
| Pick columns | `.select('id, title')` | `.select('id', 'title')` (separate args) or omit for all |
| Update | `.update({...}).eq('id', x)` | `.update(x, {...})` (by primary key `id`) |
| Delete | `.delete().eq('id', x)` | `.delete(x)` (by primary key `id`) |
| Password sign-in | `signInWithPassword(...)` | `signIn({ email, password })` |
| Current session | `await getSession()` → `data.session` | `getSession()` is **sync**, returns session or `null` |
| Errors | `error.message` | `error` is a **string** (except functions: `{ status, message }`) |
| Insert returning row | `.insert(...).select().single()` | `.insert({...})` already returns the row |
| Upsert, `.or()`, `.is()`, joins/embeds | exist | **not available**: see workarounds below |

Always check `error` before using `data`. List queries return `data` as an array; `.single()` returns one object.

Full method reference with examples: `references/sdk-reference.md`. Read it before writing auth pages, storage, realtime or edge functions.

## Database

```typescript
const { data, error } = await eb.db.from('todos')
  .select('id', 'title', 'done')
  .eq('done', false)
  .order('created_at', { ascending: false })
  .limit(20)

const { data: todo } = await eb.db.from('todos').eq('id', id).single()
await eb.db.from('todos').insert({ title, user_id: session.user.id })
await eb.db.from('todos').update(id, { done: true })
await eb.db.from('todos').delete(id)
```

Filters: `eq, neq, gt, gte, lt, lte, like, ilike, in`. Paging: `limit, offset`. Date ranges work: `.gte('created_at', a).lte('created_at', b)` applies both bounds.

Missing features and what to do instead:

- **Joins**: run two queries and combine in the client (`.in('id', ids)` for the second one), or write a Postgres view / edge function.
- **Upsert**: query first, then `insert` or `update`.
- **OR conditions / null checks**: filter client-side for small lists, or use a view or edge function.

## Creating tables and security

Every table needs `id uuid primary key default gen_random_uuid()`, because `update` and `delete` address rows by `id`. Add `created_at timestamptz default now()`. For per-user data add a `user_id uuid` column and set it from `eb.auth.getSession()?.user.id` on insert.

**Preferred: the Eurobase connector.** If the Eurobase MCP connector is connected, use its tools to inspect the schema before writing queries, and its create-table tool to make tables. Tables created that way get row-level security switched on automatically, with the `owner_access` preset when there is a `user_id`, `owner_id` or `created_by` column. Pick a preset deliberately:

| Preset | Anyone can read | Writes | Use for |
|---|---|---|---|
| `owner_access` | only the owner | only the owner | private per-user data (default) |
| `authenticated_read_owner_write` | signed-in users | only the owner | team or community data |
| `public_read_owner_write` | everyone | only the owner | public profiles, posts |
| `read_only` | everyone | nobody from the app | reference data you seed |
| `service_only` | nobody from the app | nobody from the app | data only edge functions touch |

Avoid `full_access` (anyone can write anything) unless the user explicitly asks for a public, anonymous table.

**If the connector isn't connected**, write the SQL, give it to the user to run in the Eurobase console's SQL editor, and tell them exactly where to paste it. Don't pretend the table exists.

**Raw SQL policies: always pair INSERT with SELECT.** Every insert runs as `INSERT ... RETURNING *`, and Postgres checks the SELECT policy when reading the row back. A table with an INSERT policy but no matching SELECT policy fails with "row-level security policy denied this operation", even with `WITH CHECK (true)`. The table editor in the console bypasses RLS, so "it works in the console" proves nothing.

## Auth

```typescript
await eb.auth.signUp({ email, password, emailRedirectTo: `${location.origin}/verify` })
await eb.auth.signIn({ email, password })
await eb.auth.signOut()
const session = eb.auth.getSession()            // sync, null when signed out
const unsubscribe = eb.auth.onAuthStateChange((event, session) => { /* SIGNED_IN | SIGNED_OUT | TOKEN_REFRESHED */ })
```

Build an `AuthProvider` that reads `getSession()` on mount (client-side) and subscribes with `onAuthStateChange`, cleaning up on unmount. Email verification, password reset, magic links and OAuth need their own pages (`/verify`, `/reset-password`, `/magic`, `/auth/callback`) and those URLs allowed in Eurobase: see "The app's URLs must be allowed" above and "Auth flows" in `references/sdk-reference.md`.

## Storage

Each end user owns the files they upload: only they can read, replace or delete them, and a key that already belongs to someone else is refused. Prefix keys with the user's id and show files through signed URLs (at most 1 hour with the public key):

```typescript
const { key, error } = await eb.storage.upload(`${userId}/avatar.png`, file, { contentType: file.type })
const { url } = await eb.storage.createSignedUrl(key, 'download', { expiresIn: 3600 })
```

Store the object key in the database row, not the signed URL: signed URLs expire.

**App assets everyone should see** (logos, theme images, sample files): the developer uploads them in the Eurobase console into a folder shared as **Public** (Storage → Sharing), and the app shows them with a plain URL:

```typescript
<img src={eb.storage.getPublicUrl('themes/hero.jpg')} />
```

Only developer-uploaded files in a shared folder are shared; end users can't upload into it.

**Showing one user's file to other users** (e.g. profile pictures in a member list): users can't read each other's files, so write a Eurobase edge function that runs with service access and returns short-lived signed URLs for the avatars the caller may see (or copies each upload into a public folder). Don't try to make user uploads public from the browser.

## Server-side logic: Eurobase edge functions

Payments, webhooks, emails, third-party API keys and anything needing the secret key run as Eurobase edge functions (Deno), never in the browser and never as Lovable Cloud functions. Call them from the app with `eb.functions.invoke('name', { body })`. Write the function code for the user, typed with `EdgeHandler` from `@eurobase/sdk/functions`, and tell them to deploy it in the Eurobase console (Functions) or with the Eurobase CLI; this connector can list and invoke functions but you should not assume it can deploy them. After deploying, the user can try it in the console with **Functions → Test** (optionally as one of their app's users) and see the response and `ctx.log` lines. Third-party secrets go in Eurobase Vault (console → Vault) and are read inside the function with `ctx.vault.get(...)`. Template in `references/sdk-reference.md`.

## Before calling a backend feature done

- No `supabase` imports, no Lovable Cloud tables or functions, no `eb_sk_` anywhere in the repo.
- Every table the app touches exists in Eurobase with RLS on and the right preset.
- Signed-out users can't reach private pages; one user can't read another's rows (ask the user to test with a second account).
- CORS origins + redirect URLs reminder given (preview, published and custom domain URLs).
