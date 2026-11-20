---
name: eurobase-supabase-migration
description: Use when moving an existing Supabase project (database, users, files, edge functions, scheduled jobs, app code) to Eurobase (eurobase.app). Drives the `eurobase import supabase` CLI step by step against a fresh Eurobase project, stops for the decisions that are the developer's, checks every step, and then switches the app from supabase-js to @eurobase/sdk. Load it whenever the user asks to migrate, move, port or switch from Supabase to Eurobase, or to try such a migration.
---

# Migrating a Supabase project to Eurobase

You move a working Supabase project to Eurobase with the `eurobase` CLI (0.6.9 or later). The CLI does the copying; your job is to run it in the right order, read what it reports, make the developer decide what only they can decide, check each step, and change the app's code at the end.

The developer's Supabase project keeps running throughout. Nothing here writes to Supabase, and nothing switches users over until the developer says so.

## Rules

1. **Never write to the Supabase project.** Every command reads it. Don't run SQL against it other than `SELECT`.
2. **Migrate into a fresh Eurobase project first** — a test project. Only after it checks out, do the same into the project that goes live (or keep the test project).
3. **Secrets never enter chat, code or commits.** The Supabase database URL contains a password, and the service role key is an admin key. Ask the developer to put them in environment variables in *their* terminal (or a `.env` that git ignores), and refer to them by name. If one is pasted into the conversation, tell the developer to rotate it after the migration. Never print them, never write them into files you create.
4. **Stop and ask at every decision point below** (marked **Decision**). Don't pick for the developer, and don't pass `--accept-access-changes`, `--deploy`, `--apply` or `--enable` on your own, and never run `eurobase keys rotate-secret` or `regenerate` yourself.
5. **Copying real users or real data is a decision** too: a test project then holds personal data. Say so, and offer `--exclude-table` to leave large or sensitive tables out of a first test.
6. **Report what each command printed** — especially its "Read before applying" / "needs changes" lists. They are the migration's to-do list.

## Before you start

Ask the developer for, or check:

- The Eurobase CLI: `eurobase --version` (0.6.9+; install from console → Docs → CLI). Signed in: `eurobase login` — the developer types their password themselves.
- `pg_dump` on PATH, the same major version as the Supabase database or newer (`brew install libpq` on macOS).
- In the developer's shell (not in chat):
  - `SUPABASE_DB_URL` — Supabase dashboard → **Connect** → **Session pooler** URL (the direct `db.<ref>.supabase.co` host is IPv6-only).
  - `SUPABASE_URL` (`https://<ref>.supabase.co`) and `SUPABASE_SERVICE_ROLE_KEY` — only needed to copy files.
- The Supabase project folder (with `supabase/functions/` and `supabase/config.toml`), if it has edge functions. `supabase functions download <name>` fetches one that isn't in git.
- A target project: `eurobase projects create <name>`, then `eurobase switch <name>`. Check `eurobase projects list` — the `*` marks the active project, and **every command below acts on it**. Re-check before each step that writes.
- For the checks, the project's keys in the developer's shell, by name: `EB_URL` (`https://<slug>.eurobase.app`), `EB_PUBLIC_KEY` (`eurobase keys show`) and `EB_SECRET_KEY`. The secret key is shown once — in the console when the project is created, or when the **developer** runs `eurobase keys rotate-secret --confirm` in their own terminal (it prints the new key; fine on a fresh test project, on a project in use it breaks whatever uses the old key). The developer exports them before starting Claude Code (or puts them in a git-ignored `.env` you `source`); you use them by name.

**Run everything from a migration folder, not from the repository root:**

```bash
mkdir migration && cd migration
```

`eurobase migrations up` applies `./migrations` of the current folder, and many apps already have a `migrations/` folder of their own — run from the root, the import would land next to the app's migrations and apply them too. From `migration/`, point at the app's files with `../`: `--functions-dir ../supabase/functions`, `--input ../supabase/functions`. Generated files contain user data from Step 3 on; make sure `migration/` isn't committed (add it to `.gitignore`).

**Plan:** Free projects allow 50 edge functions, Pro 500, Team unlimited; database and file storage limits are in console → Usage. Compare with what Step 1 reports before going further.

## Step 1 — Assess (read-only)

```bash
eurobase import supabase assess --functions-dir ../supabase/functions --output assess-report.md
```

It writes a Markdown report: tables, policies, functions, triggers, extensions, users, buckets, edge functions — each graded ✅ / ⚠️ / ❌. The same object can appear in several sections; summarise by object, not by line. Summarise it for the developer: the blockers first, then the warnings, then the size (rows, users, files) against the plan. Typical findings and what they mean:

- **SECURITY DEFINER functions** (❌ in the report) — the schema step converts them for you; ❌ means "review", not "refused". They become normal functions running with the caller's rights. Anything the app calls that relied on the owner's rights must move to server code (an edge function with `ctx.db.asService()`, or the secret key from a backend), or be closed to app users with the guard in Step 2.
- **Triggers on `auth.users`** (e.g. `handle_new_user` creating a profile) — Eurobase has no such trigger point. That work moves into the sign-up code or an edge function.
- **GRANT / REVOKE restrictions** (a column hidden from `anon`) — not carried over; a view doesn't hide columns on Eurobase. Restricted columns move to their own table with stricter policies, or behind an edge function.
- **Owner-run views** — become `security_invoker` views (they apply RLS for the caller).
- **Extensions** not available — listed with what to use instead.
- **Database webhooks** (`pg_net` / `supabase_functions.http_request` triggers, e.g. on `auth.users`) — their definitions can contain secret URLs (a Slack incoming webhook, a token in a query string). Don't print the URL; say where it points by host. They become Eurobase webhooks (console → Webhooks) or code.

Also look at the app itself — the report covers the database, not the code:

- **Direct database connections.** `grep -rnE "pg-boss|from 'pg'|require\('pg'\)|from 'postgres'|DATABASE_URL|prisma|drizzle|knex|kysely|typeorm|sequelize"` — Free and Pro projects never get a database URL (data goes through the SDK and REST); a Team project's dedicated database comes with an owner URL. A queue like pg-boss that needs its own Postgres keeps it (a small EU Postgres used only for the queue), or its jobs move to Eurobase cron jobs + edge functions. Tell the developer early: it can decide the plan.
- **App-level schedulers** (pg-boss, node-cron, BullMQ, Cloud Scheduler) — Step 6 only carries pg_cron over.
- **A backend that uses supabase-js on the server** (`auth.admin.*`, `createClient` with a user's token, `auth.getUser(jwt)`) — see Step 7, "Backends".

## Step 2 — Schema

```bash
eurobase import supabase schema --migrations-dir ./migrations     # from migration/
```

If access rules can't be carried over, it refuses to write and lists them. **Decision:** go through the list with the developer. Only when they accept it, run again with `--accept-access-changes`. The accepted list is written at the top of the migration file.

Before asking, work out for each item what it means in practice (read-only queries on Supabase are fine):

- **A revoked EXECUTE** (`anon` / `authenticated` couldn't call a function): on Eurobase any app caller could call it with `eb.db.rpc`. Who calls it in the code — the browser, or a backend with the service role key? What does it write, and do that table's policies stop an app user anyway (RLS on, no policy = nobody but the service role)?
- **SECURITY DEFINER → invoker**: what did it need the owner's rights for? A trigger fired by service-role writes keeps working; one fired by app users' writes now writes with their rights.
- Offer the guard below for every function only the server should call.

**Guard: functions only the secret key may call.** Write a migration after the schema with `CREATE OR REPLACE` of each such function, body unchanged, plus a check at the start:

```sql
CREATE OR REPLACE FUNCTION claim_trial(p_user_id uuid) RETURNS boolean
LANGUAGE plpgsql AS $$
BEGIN
  IF NOT public.is_service_role() THEN
    RAISE EXCEPTION 'claim_trial may only be called with the secret key' USING ERRCODE = '42501';
  END IF;
  -- … the original body …
END $$;
```

Write `public.is_service_role()` qualified — edge functions and cron jobs run with only the project's schema on the search path. A `LANGUAGE sql` function can't raise: rewrite it as `plpgsql` — `RETURN (<the SELECT>)` for a single value, `RETURN QUERY <the SELECT>` for a set, plain statements (`PERFORM` for a SELECT whose result isn't used) for `void`; same signature and result. Create the file with `eurobase migrations new service_only_functions` so it sorts after the schema migration. App callers get HTTP 403 with code `42501` ("permission denied"; the function's own message isn't passed on). Show the migration to the developer before applying it.

**Check the guard** with curl, keys by name: the public key must get 403 for every guarded function, the secret key must get past the check (call only functions that write nothing with test arguments), and one unguarded function must still answer the public key — that shows the 403 comes from the guard.

```bash
curl -s -X POST "$EB_URL/v1/db/rpc/claim_trial" -H "apikey: $EB_PUBLIC_KEY" \
  -H 'Content-Type: application/json' -d '{"p_user_id":"00000000-0000-0000-0000-000000000000"}'
```

Then apply and check:

```bash
eurobase migrations up
```

What the import does that the developer should know: `public.` prefixes are removed (the project has its own schema), `auth.uid()` becomes `auth_uid()`, and every table with RLS gets a `eurobase_service_role` policy so the secret key (service role) can read and write it as Supabase's service role could. App users keep exactly what their policies allow.

**Every new project has a sample `todos` table.** If the Supabase project has its own `todos`, drop the sample table first (console → Database, or `DROP TABLE todos` in the SQL editor) before applying.

If `migrations up` fails, read the error. A missing table usually means the Supabase schema changed after the file was written — write it again.

## Step 3 — Users and data

```bash
eurobase import supabase data --migrations-dir ./migrations
```

Users come first (ids, emails, bcrypt password hashes, OAuth links), then every table's rows, parents before children. Sessions are not copied: every user signs in once on Eurobase. The import is for a project without data: it is not incremental, and applying it twice writes the rows twice — also if you run the import twice before applying (the second run writes a second set of files). The data comes as several migration files of at most about 450 KB each.

**Decision (test runs):** to leave large tables out — logs, analytics — use `--exclude-table logs,events`. The command refuses if another table's rows refer to an excluded one; exclude that too or keep both.

Read the "Read before applying" list out loud — its lines appear only when they apply: deleted and SSO users left out, password hashes that can't be used (those users reset their password), users without a password (they sign in only with their OAuth provider, so it must be set up), OAuth providers that need setting up, triggers paused during the copy.

**Decision:** the files hold real users (emails, password hashes) and their data. Apply only after the developer agrees to copy them into this project:

```bash
eurobase migrations up
```

**Check:** compare row counts per table between Supabase (`psql "$SUPABASE_DB_URL" -v ON_ERROR_STOP=1 -c 'SET default_transaction_read_only = on' -c '<SELECT …>'` — separate `-c` flags, so the read-only setting is in force before the query) and Eurobase (`eurobase db query "<sql>" --json`, the console SQL editor, or the MCP server's `runSQL`). For the important tables compare a checksum of **each column**, not of whole rows — the two databases sort text differently. A sum of hashes doesn't depend on row order:

```sql
SELECT count(*) AS n, count(col) AS non_null,
       sum(('x' || left(md5(col::text), 15))::bit(60)::bigint)::text AS h
FROM t;
```

(`::text` because JSON output rounds big numbers.)

Use `extract(epoch FROM col)` instead of `col::text` for timestamps (the text form depends on the session's time zone). For users, compare `auth.users` on Supabase with `users` on Eurobase: `id`, `email`, `encrypted_password` ↔ `password_hash`, `email_confirmed_at`, `created_at`, `last_sign_in_at`. The import changes some of it, so on the Supabase side use `WHERE deleted_at IS NULL AND NOT is_sso_user`, `lower(trim(email))` and `nullif(encrypted_password, '')`, and expect the users whose hashes "Read before applying" listed as unusable to differ.

## Step 4 — Files (storage)

```bash
eurobase import supabase storage                 # report: buckets, files, sizes
eurobase import supabase storage --apply         # copy
```

**Decision:** before `--apply`, show the report. Point out:
- each bucket becomes a folder (`avatars/u1/me.png` stays `avatars/u1/me.png`);
- files keep their owners (import the users first);
- a **public** bucket becomes a folder shared with everyone, and its files become developer files — their uploaders can no longer replace or delete them from the app;
- the copy isn't checked against the plan's storage limit — compare the total first.

The copy streams each file straight from Supabase to the project; it can be stopped and run again (files already there are skipped; nothing in the project is ever replaced). Supabase's `.emptyFolderPlaceholder` objects are skipped by the copy, so the report counts one more object for each folder that has one. **Check:** run `--apply` a second time — it should report every file as already there. Spot-check a few files: `eurobase storage download <bucket>/<path> /tmp/a`, the same file from Supabase (`curl -s -H "Authorization: Bearer $SUPABASE_SERVICE_ROLE_KEY" "$SUPABASE_URL/storage/v1/object/<bucket>/<path>" -o /tmp/b`, a read), compare `md5`, then delete both copies.

## Step 5 — Edge functions

```bash
eurobase import supabase functions --input ../supabase/functions
```

It builds every function the way a deploy would and reports each as ready or not. **"Ready" means it builds**, not that it works: check that the tables each function reads (`.from('<table>')`, `rpc('<fn>')`) exist in the imported schema, and whether anything still calls the function (`functions.invoke('<name>'` in the app, webhooks, cron). Functions that use missing tables or that nothing calls are usually dead code — tell the developer, and deploy only what's used. Functions run **as written**: `Deno.serve`, `Deno.env.get`, the `Authorization` header and `createClient(url, key)` with `auth.getUser`, `from().select/insert/update/upsert/delete`, `rpc()` and storage are supplied by a compatibility layer; imports (`https://`, `npm:`, `jsr:`, `../_shared/`) are bundled in.

For a function listed as "needs changes", the report names the file, the line and what to use instead — change those lines with the developer (calling another function with `functions.invoke`, realtime channels, `select()` with an embedded table, `or()` filters, most `auth.admin` calls, a direct database connection, requests to the Supabase API by URL).

**Secrets:** the report lists the names the functions read. The developer sets each one — never you, and never in chat:

Console → Vault, or in their own terminal (a leading space keeps the value out of shell history in most shells):

```bash
 eurobase vault set STRIPE_SECRET_KEY '<value>'
```

**Decision:** then deploy the ready ones:

```bash
eurobase import supabase functions --input ../supabase/functions --deploy --only <name>,<name>
```

Functions that use the service role key are deployed with the allow-service-role setting; `verify_jwt = false` in `supabase/config.toml` is respected. Without a `config.toml`, every function requires a signed-in caller — ask the developer which ones (webhooks from Stripe, say) take calls without one. **Check:** call one function as a signed-in user and one without (expect 401 where sign-in is required); read `eurobase edge-functions logs <name>`.

Free projects allow 50 functions, Pro 500.

## Step 6 — Scheduled jobs (pg_cron)

```bash
eurobase import supabase cron                    # plan
eurobase import supabase cron --apply            # create, switched off
```

SQL commands become SQL jobs; `net.http_post('…/functions/v1/<name>', …)` becomes a job that runs that function with the JSON body as payload. Jobs are created **switched off** — the same jobs still run on Supabase. Anything that can't be carried over unchanged is listed with the reason. Schedules that live in the app (pg-boss, node-cron, a cloud scheduler) aren't seen here — see Step 1.

## Step 7 — The app's code

Replace `@supabase/supabase-js` with `@eurobase/sdk`. Don't rename things that don't need renaming; the shape is close. Key differences:

| supabase-js | @eurobase/sdk |
|---|---|
| `createClient(url, anonKey)` | `createClient({ url, apiKey: publicKey, projectId })` |
| `auth.signUp({ email, password, options: { data, emailRedirectTo } })` | `eb.auth.signUp({ email, password, metadata, emailRedirectTo })` |
| `auth.signInWithPassword({ email, password })` | `eb.auth.signIn({ email, password })` |
| `auth.signInWithOAuth({ provider: 'google', options: { redirectTo } })` | `eb.auth.signInWithOAuth('google', { redirectTo })`, then `eb.auth.handleOAuthCallback()` on the callback page |
| `auth.signInWithOtp({ email })` | `eb.auth.requestMagicLink(email, { emailRedirectTo })`, then `eb.auth.signInWithMagicLink(token)` on the link's page |
| `auth.resetPasswordForEmail(email, { redirectTo })` | `eb.auth.forgotPassword(email, { emailRedirectTo })`, then `eb.auth.resetPassword(token, newPassword)` |
| `auth.updateUser({ password })` | `eb.auth.updatePassword({ currentPassword, newPassword })` (needs the current password) |
| `auth.signOut()` | `eb.auth.signOut()` |
| `auth.resend({ type: 'signup', email })` | `eb.auth.resendVerification(email, { emailRedirectTo })` |
| `(await auth.getSession()).data.session` | `eb.auth.getSession()` (synchronous) |
| `(await auth.getUser()).data.user` | `(await eb.auth.getUser()).data` |
| `auth.onAuthStateChange(cb)` → `data.subscription.unsubscribe()` | `const off = eb.auth.onAuthStateChange(cb)` → `off()` |
| `from('t').select('id, name')` | `eb.db.from('t').select('id', 'name')` |
| `from('t').select('*, author:profiles(*)')` (to-one) | `eb.db.from('t').select('*', 'profiles(*)')` — named by the **table**, no alias or `!fk` hint; one level, along a foreign key of `t`; with two foreign keys to the same table, one of them is used — load the other with a second query. `users(*)` is refused from the app |
| `from('t').select('*, comments(*)')` (to-many) | a second query: `eb.db.from('comments').in('post_id', ids)` |
| `.eq / .neq / .gt / .gte / .lt / .lte / .like / .ilike / .in / .order / .limit` | the same |
| `.range(from, to)` | `.offset(from).limit(to - from + 1)` |
| `.single()` | `.single()` (error `'Row not found'` when there's no row; with several rows it returns the first, no error) |
| `.match({ a: 1, b: 2 })` / `.filter('a', 'eq', 1)` | `.eq('a', 1).eq('b', 2)` — the method of that operator |
| `select('*', { count: 'exact' })` / `{ count: 'exact', head: true }` | every list returns `count` (all matching rows); for the count alone, `.limit(1)` and read `count` |
| `.maybeSingle()` | `.single()` and treat `'Row not found'` as "none" |
| `.or()`, `.not()`, `.is()`, `.contains()`, `.textSearch()` | a database function called with `eb.db.rpc` (or an edge function). `rpc` returns only the first row of a set-returning function: return the list as one `jsonb` value (`jsonb_agg`) |
| `from('t').insert(row).select().single()` | `eb.db.from('t').insert(row)` (returns the row) |
| `from('t').insert([rows])` | one `insert` per row, or a database function for bulk inserts |
| `from('t').upsert(row)` | a database function (`INSERT … ON CONFLICT`) called with `rpc` |
| `from('t').update(values).eq('id', id)` | `eb.db.from('t').update(id, values)` (by id only) |
| `from('t').delete().eq('id', id)` | `eb.db.from('t').delete(id)` (by id only) |
| `update(values).eq('id', id).eq('user_id', uid)` (owner filter) | as the user (RLS decides): `update(id, values)`; with the secret key, check the owner first or use a database function |
| `update(values).eq('status', 'old')` (many rows) | a database function called with `rpc` |
| `rpc(fn, args)` | `eb.db.rpc(fn, args)` → `{ data, error, code }`; a function returning `SETOF` / `TABLE` gives only its first row — return `jsonb` instead |
| `storage.from('b').upload(path, file)` | `eb.storage.upload('b/' + path, file, { contentType })` (one bucket per project; the old bucket is a folder) |
| `storage.from('b').createSignedUrl(path, 60)` | `eb.storage.createSignedUrl('b/' + path, 'download', { expiresIn: 60 })` |
| `storage.from('b').createSignedUploadUrl(path)` | `eb.storage.createSignedUrl('b/' + path, 'upload')` |
| `storage.from('b').getPublicUrl(path)` | `eb.storage.getPublicUrl('b/' + path)` (folders shared with everyone; public key only) |
| `storage.from('b').download(path)` | `eb.storage.download('b/' + path)` → `Blob`; it throws on an error instead of returning `{ error }` |
| `storage.from('b').list(folder)` | `eb.storage.list({ prefix: 'b/' + folder + '/' })` → `{ objects, has_more, next_cursor }` — every file under the prefix, subfolders included, no folder entries |
| `storage.from('b').remove([paths])` | `eb.storage.remove('b/' + path)` per file |
| `functions.invoke(name, { body })` | `eb.functions.invoke(name, { body })` |
| `channel(…).on('postgres_changes', { event, schema, table }, cb).subscribe()` | `const sub = eb.realtime.on(table, 'INSERT' \| 'UPDATE' \| 'DELETE' \| '*', cb)` → `eb.realtime.off(sub)`. Needs `projectId` in `createClient`. **Custom RLS policies don't apply to realtime:** signed-in users get only their own rows on tables with an owner column (`user_id`, `owner_id`, `created_by`, `uploaded_by`), and nothing else is filtered — tell the developer which subscribed tables relied on Supabase's realtime RLS |

Errors are plain strings (`toast.error(error)`, not `error.message`), except `functions.invoke`, whose `error` has a `message`. A file in a folder shared with everyone (a former public bucket) has a plain URL: `eb.storage.getPublicUrl('bucket/' + path)`. Before writing calls you're unsure of, read the SDK reference: https://github.com/STGime/euroback/blob/main/integrations/lovable/references/sdk-reference.md (written for Lovable apps; the calls are the same everywhere), or console → Docs.

Environment: the public key (`eb_pk_…`) and project URL/ID go into the app; the **secret key (`eb_sk_…`) never does** — it belongs in a backend or an edge function. Allowed CORS origins and redirect URLs for the app's domains are set in console → Auth.

For OAuth: set each provider up in console → Auth with the **same OAuth app (client ID)** as on Supabase, so imported links keep working. Try one sign-in per provider.

Password reset: Supabase signs the user in from the reset link and then calls `updateUser({ password })`; Eurobase's link carries a token for `eb.auth.resetPassword(token, newPassword)` — the reset page changes.

### Backends (supabase-js on a server)

A browser client holds one signed-in user. On a server, never share a signed-in client between requests — one user's session would serve the next request. Read console → Docs → *Using the SDK on a server* (chapter 32) and *User admin API (server-side)* (chapter 33) before changing a backend. A backend usually needs **two clients**: one with the secret key (service role, `eb.auth.admin`), one with the public key for acting as users.

| supabase-js on a server | @eurobase/sdk |
|---|---|
| `createClient(url, serviceRoleKey, { auth: { persistSession: false } })` | `admin = createClient({ url, apiKey: process.env.EB_SECRET_KEY, projectId, auth: { persistSession: false } })` — one per process, never signed in; runs as the service role |
| `createClient(url, anonKey, { global: { headers: { Authorization: 'Bearer ' + jwt } } })` (act as the user, RLS applies) | `eb = createClient({ url, apiKey: process.env.EB_PUBLIC_KEY, projectId, auth: { persistSession: false } })` once, then `eb.withAccessToken(jwt)` per request (it refuses a secret-key client) |
| `auth.getUser(jwt)` (check a caller's token) | `eb.auth.getUser(jwt)` — like-for-like: asks the server, so it notices deleted and suspended users; `eb.auth.verifyAccessToken(jwt)` is the fast local check (signature and expiry only) |
| `auth.signInWithPassword` / `signUp` / `refreshSession({ refresh_token })` proxied for a client | `signIn` / `signUp` / `refreshSession(refreshToken)` on the public-key client with `persistSession: false`; return the session to the client |
| `auth.admin.createUser({ email, password, email_confirm, user_metadata })` | `admin.auth.admin.createUser({ email, password, emailConfirmed, metadata, displayName })` — **`emailConfirmed` defaults to true** (Supabase: false) |
| `auth.admin.listUsers()` / `getUserById(id)` / `deleteUser(id)` | `admin.auth.admin.listUsers({ page, perPage, search })` → `{ users, total }` / `getUserById(id)` / `deleteUser(id)` |
| `auth.admin.updateUserById(id, { email, user_metadata })` | `admin.auth.admin.updateUserById(id, { email, metadata, displayName, emailConfirmed })` — `metadata` replaces the whole object |
| `auth.admin.updateUserById(id, { password })` | `admin.auth.admin.setPassword(id, password)` |
| `auth.admin.signOut(jwt)` / `updateUserById(id, { ban_duration })` | `admin.auth.admin.signOutUser(id)` / `suspendUser(id)` (access tokens already issued stay valid until they expire) |
| `auth.exchangeCodeForSession(code)` (server-side OAuth) | not available — OAuth sign-in finishes in the browser (`handleOAuthCallback`); send the session to the backend from there |
| `auth.admin.generateLink`, `inviteUserByEmail` | not available — `createUser` + `eb.auth.forgotPassword(email)` sends a set-password link |

Triggers on `auth.users` (a profile row at sign-up) become code: after `signUp` / `admin.createUser` — and on the first OAuth sign-in — call a database function that creates the rows; close it with the guard from Step 2.

## Step 8 — Switch over

**Decision:** the developer picks the moment. The production project is a new, empty project; the test project stays a test.

Before the switch (Supabase keeps running):

1. Create the production project and make it active (`eurobase projects create`, `eurobase switch`).
2. In a **new folder**, put the schema migration the test project used into `migrations/` — not the test's data files — and run `eurobase migrations up`. (The data import writes new files next to old ones; applying old and new data together writes rows twice.)
3. Copy the files (`storage --apply`), have the developer set the secrets, deploy the functions, import the cron jobs (switched off), and set up auth providers, CORS origins and redirect URLs — steps 4–7 against the production project.

The switch:

4. The developer stops writes on Supabase (maintenance mode in the app, or a quiet hour).
5. In the new folder: `eurobase import supabase data --migrations-dir ./migrations`, then — after the developer's go — `eurobase migrations up`. Compare row counts with Supabase.
6. Run `storage --apply` once more: it copies only new files.
7. The developer switches the cron jobs on in console → Cron, and switches them off on Supabase themselves (you never write to Supabase).
8. Deploy the app with the Eurobase keys.
9. Keep the Supabase project untouched for a while as the way back.

## What to tell the developer at the end

- What was copied (counts), what was left out and why, and how it was checked.
- Access changes they accepted, and what in the app depends on them.
- Secrets they still need to set, OAuth providers to configure, functions that needed changes, functions that look unused.
- What the app's code needs (the Step 7 plan), including direct database connections and app-level schedulers.
- That a test project holding real users should be deleted when they're done with it: `eurobase projects delete <id> --confirm` — their decision.
