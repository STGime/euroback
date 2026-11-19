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
4. **Stop and ask at every decision point below** (marked **Decision**). Don't pick for the developer, and don't pass `--accept-access-changes`, `--deploy`, `--apply` or `--enable` on your own.
5. **Copying real users or real data is a decision** too: a test project then holds personal data. Say so, and offer `--exclude-table` to leave large or sensitive tables out of a first test.
6. **Report what each command printed** — especially its "Read before applying" / "needs changes" lists. They are the migration's to-do list.

## Before you start

Ask the developer for, or check:

- The Eurobase CLI: `eurobase --version` (0.6.9+; install from console → Docs → CLI). Signed in: `eurobase login` — the developer types their password themselves.
- `pg_dump` on PATH, the same major version as the Supabase database (`brew install libpq` on macOS).
- In the developer's shell (not in chat):
  - `SUPABASE_DB_URL` — Supabase dashboard → **Connect** → **Session pooler** URL (the direct `db.<ref>.supabase.co` host is IPv6-only).
  - `SUPABASE_URL` (`https://<ref>.supabase.co`) and `SUPABASE_SERVICE_ROLE_KEY` — only needed to copy files.
- The Supabase project folder (with `supabase/functions/` and `supabase/config.toml`), if it has edge functions. `supabase functions download <name>` fetches one that isn't in git.
- A target project: `eurobase projects create <name>`, then `eurobase switch <name>`. Check `eurobase projects list` — the `*` marks the active project, and **every command below acts on it**. Re-check before each step that writes.

Work in a scratch folder (e.g. `migration/`) so generated files don't mix with the app's code.

## Step 1 — Assess (read-only)

```bash
eurobase import supabase assess --functions-dir ./supabase/functions
```

It writes a Markdown report: tables, policies, functions, triggers, extensions, users, buckets, edge functions — each graded ✅ / ⚠️ / ❌. Summarise it for the developer: the blockers first, then the warnings, then the size (rows, users, files). Typical findings and what they mean:

- **SECURITY DEFINER functions** — not allowed on Eurobase; they become normal functions running with the caller's rights. Anything the app calls that relied on the owner's rights must move to server code (an edge function with `ctx.db.asService()`, or the secret key from a backend).
- **Triggers on `auth.users`** (e.g. `handle_new_user` creating a profile) — Eurobase has no such trigger point. That work moves into the sign-up code or an edge function.
- **GRANT / REVOKE restrictions** (a column hidden from `anon`) — not carried over; a view doesn't hide columns on Eurobase. Restricted columns move to their own table with stricter policies, or behind an edge function.
- **Owner-run views** — become `security_invoker` views (they apply RLS for the caller).
- **Extensions** not available — listed with what to use instead.

## Step 2 — Schema

```bash
eurobase import supabase schema --migrations-dir ./migrations
```

If access rules can't be carried over, it refuses to write and lists them. **Decision:** go through the list with the developer. Only when they accept it, run again with `--accept-access-changes`. The accepted list is written at the top of the migration file.

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
eurobase migrations up
```

Users come first (ids, emails, bcrypt password hashes, OAuth links), then every table's rows, parents before children. Sessions are not copied: every user signs in once on Eurobase.

**Decision (test runs):** to leave large tables out — logs, analytics — use `--exclude-table logs,events`. The command refuses if another table's rows refer to an excluded one; exclude that too or keep both.

Read the "Read before applying" list out loud: deleted and SSO users left out, password hashes that can't be used (those users reset their password), OAuth providers that need setting up, triggers paused during the copy.

**Check:** compare row counts per table between Supabase (`SELECT count(*) …` there) and Eurobase (console SQL editor, or the MCP server's `runSQL`). For a few important tables compare a checksum of each column, not of whole rows — the two databases sort text differently.

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

The copy streams each file straight from Supabase to the project; it can be stopped and run again (files already there are skipped; nothing in the project is ever replaced). **Check:** run `--apply` a second time — it should report every file as already there. Spot-check a few downloads.

## Step 5 — Edge functions

```bash
eurobase import supabase functions --input ./supabase/functions
```

It builds every function the way a deploy would and reports each as ready or not. Functions run **as written**: `Deno.serve`, `Deno.env.get`, the `Authorization` header and `createClient(url, key)` with `auth.getUser`, `from().select/insert/update/upsert/delete`, `rpc()` and storage are supplied by a compatibility layer; imports (`https://`, `npm:`, `jsr:`, `../_shared/`) are bundled in.

For a function listed as "needs changes", the report names the file, the line and what to use instead — change those lines with the developer (calling another function with `functions.invoke`, realtime channels, `select()` with an embedded table, `or()` filters, most `auth.admin` calls, a direct database connection, requests to the Supabase API by URL).

**Secrets:** the report lists the names the functions read. The developer sets each one — never you, and never in chat:

```bash
eurobase vault set STRIPE_SECRET_KEY <value>
```

**Decision:** then deploy the ready ones:

```bash
eurobase import supabase functions --input ./supabase/functions --deploy
```

Functions that use the service role key are deployed with the allow-service-role setting; `verify_jwt = false` in `supabase/config.toml` is respected. **Check:** call one function as a signed-in user and one without (expect 401 where sign-in is required); read `eurobase edge-functions logs <name>`.

Free projects allow 3 functions, Pro 500.

## Step 6 — Scheduled jobs (pg_cron)

```bash
eurobase import supabase cron                    # plan
eurobase import supabase cron --apply            # create, switched off
```

SQL commands become SQL jobs; `net.http_post('…/functions/v1/<name>', …)` becomes a job that runs that function with the JSON body as payload. Jobs are created **switched off** — the same jobs still run on Supabase. Anything that can't be carried over unchanged is listed with the reason.

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
| `(await auth.getSession()).data.session` | `eb.auth.getSession()` (synchronous) |
| `(await auth.getUser()).data.user` | `(await eb.auth.getUser()).data` |
| `auth.onAuthStateChange(cb)` → `data.subscription.unsubscribe()` | `const off = eb.auth.onAuthStateChange(cb)` → `off()` |
| `from('t').select('id, name')` | `eb.db.from('t').select('id', 'name')` |
| `from('t').select('*, author(*)')` (to-one) | `eb.db.from('t').select('*', 'author(*)')` — one level, along a foreign key of `t` |
| `from('t').select('*, comments(*)')` (to-many) | a second query: `eb.db.from('comments').in('post_id', ids)` |
| `.eq / .neq / .gt / .gte / .lt / .lte / .like / .ilike / .in / .order / .limit` | the same |
| `.range(from, to)` | `.offset(from).limit(to - from + 1)` |
| `.single()` | `.single()` (error `'Row not found'` when there's no row) |
| `.maybeSingle()` | `.single()` and treat `'Row not found'` as "none" |
| `.or()`, `.not()`, `.is()`, `.contains()`, `.textSearch()` | a database function called with `eb.db.rpc` (or an edge function) |
| `from('t').insert(row).select().single()` | `eb.db.from('t').insert(row)` (returns the row) |
| `from('t').insert([rows])` | one `insert` per row, or a database function for bulk inserts |
| `from('t').upsert(row)` | a database function (`INSERT … ON CONFLICT`) called with `rpc` |
| `from('t').update(values).eq('id', id)` | `eb.db.from('t').update(id, values)` (by id only) |
| `from('t').delete().eq('id', id)` | `eb.db.from('t').delete(id)` (by id only) |
| `rpc(fn, args)` | `eb.db.rpc(fn, args)` → `{ data, error, code }` |
| `storage.from('b').upload(path, file)` | `eb.storage.upload('b/' + path, file, { contentType })` (one bucket per project; the old bucket is a folder) |
| `storage.from('b').createSignedUrl(path, 60)` | `eb.storage.createSignedUrl('b/' + path, 'download', { expiresIn: 60 })` |
| `storage.from('b').createSignedUploadUrl(path)` | `eb.storage.createSignedUrl('b/' + path, 'upload')` |
| `storage.from('b').getPublicUrl(path)` | `eb.storage.getPublicUrl('b/' + path)` (folders shared with everyone; public key only) |
| `storage.from('b').download(path)` | `eb.storage.download('b/' + path)` → `Blob` |
| `storage.from('b').list(folder)` | `eb.storage.list({ prefix: 'b/' + folder + '/' })` → `{ objects, has_more, next_cursor }` |
| `storage.from('b').remove([paths])` | `eb.storage.remove('b/' + path)` per file |
| `functions.invoke(name, { body })` | `eb.functions.invoke(name, { body })` |
| `channel(…).on('postgres_changes', { event, table }, cb).subscribe()` | `const sub = eb.realtime.on(table, 'INSERT' \| 'UPDATE' \| 'DELETE' \| '*', cb)` → `eb.realtime.off(sub)` |

Errors are plain strings (`toast.error(error)`, not `error.message`), except `functions.invoke`, whose `error` has a `message`. A file in a folder shared with everyone (a former public bucket) has a plain URL: `eb.storage.getPublicUrl('bucket/' + path)`. Before writing calls you're unsure of, read the SDK reference: https://github.com/STGime/euroback/blob/main/integrations/lovable/references/sdk-reference.md (written for Lovable apps; the calls are the same everywhere), or console → Docs.

Environment: the public key (`eb_pk_…`) and project URL/ID go into the app; the **secret key (`eb_sk_…`) never does** — it belongs in a backend or an edge function. Allowed CORS origins and redirect URLs for the app's domains are set in console → Auth.

For OAuth: set each provider up in console → Auth with the **same OAuth app (client ID)** as on Supabase, so imported links keep working. Try one sign-in per provider.

## Step 8 — Switch over

**Decision:** the developer picks the moment. In order:

1. Stop writes on Supabase (maintenance mode in the app, or a quiet hour).
2. Run `data` again **into a fresh project** for the final copy — the data import is not incremental (or, if Supabase barely changed, copy only the tables that changed with `--table`).
3. Run `storage --apply` once more: it copies only new files.
4. Switch the cron jobs on in console → Cron, and switch them off on Supabase.
5. Deploy the app with the Eurobase keys.
6. Keep the Supabase project untouched for a while as the way back.

## What to tell the developer at the end

- What was copied (counts), what was left out and why.
- Access changes they accepted, and what in the app depends on them.
- Secrets they still need to set, OAuth providers to configure, functions that needed changes.
- That a test project holding real users should be deleted when they're done with it: `eurobase projects delete <id> --confirm` — their decision.
