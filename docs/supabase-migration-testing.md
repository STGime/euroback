# Supabase → Eurobase Migration — Test Plan

Umbrella: [#267]. Sub-issues: [#268] assess, [#269] schema, [#270] data,
[#271] storage, [#272] functions. All merged. `assess` now enumerates
storage buckets (from the Postgres `storage` schema) and grades local
edge functions (with the `functions` step's check) — no longer stubbed.
**Not** yet end-to-end validated against a real Supabase project.

Purpose of this document: turn the CLI into a beta feature we're
willing to point tenants at. Nothing here is user-facing — it's the
runbook for the person doing the smoke test.

---

## 1. Prerequisites

### 1.1 Source: a real Supabase project you own

- Free tier is fine. Ideally: a **retired side project** so a rogue
  DELETE from a `psql` misclick doesn't matter. Anything with real
  auth users + at least one table + at least one bucket + at least
  one edge function covers all five subcommands.
- Grab these from the Supabase console (Project Settings → Database
  and Project Settings → API):
  - `SUPABASE_DB_URL` — the Postgres connection string (Session pooler
    URL, NOT the direct connection which requires a static IP).
  - `SUPABASE_SERVICE_KEY` — the `service_role` key from the API tab.
    `assess` does **not** need it (it reads storage buckets over the
    Postgres connection and grades edge functions from your local
    `./supabase/functions` tree). Later steps that write to Eurobase use
    it.

If you have nothing suitable, spin up a fresh Supabase project and
seed it with:

- 3 tables with FK relationships (`users_profile → orders → line_items`)
- 5–10 auth users (mix of email/password + OAuth if possible)
- 2 storage buckets (one public, one private) with a handful of
  objects each
- 2 edge functions (one plain, one that imports `@supabase/supabase-js`)

### 1.2 Target: a fresh Eurobase project

- Create a new project via the console; do **not** reuse a project
  with real tenants on it. The whole point is a destructive-ok test.
- Store the project's URL + anon key + service key for verification
  steps.
- Confirm `eurobase migrations up` works against it before starting
  (apply a trivial no-op migration).

### 1.3 Local tooling

- Latest `eurobase` CLI built from `main` (`go install ./cmd/eurobase`).
- `pg_dump` on PATH (the `schema` subcommand shells out to it —
  `brew install libpq && brew link --force libpq` on macOS).
- `rclone` on PATH (`brew install rclone` / `apt install rclone`).
- Two rclone remotes configured (`rclone config`):
  - `supabase_src` → S3-compatible endpoint
    `https://<project>.supabase.co/storage/v1/s3` (region `us-east-1`,
    access + secret from the Supabase Storage → API Keys page).
  - `eurobase_dst` → Scaleway S3 endpoint
    `https://s3.fr-par.scw.cloud` (region `fr-par`, credentials from
    the Eurobase console Storage → Credentials tab).

---

## 2. Step 1 — `assess` (read-only, low-risk)

**Goal:** produce a compat report against the source project. Confirms
credentials + connectivity, and gives us a baseline of what should
land where.

```bash
export SUPABASE_DB_URL='postgresql://…'
# assess needs no service key. To grade edge functions, point
# --functions-dir at your local Supabase functions folder (default
# ./supabase/functions); run `supabase functions download <name>` first,
# or omit it and the Edge functions section is skipped with a note.
eurobase import supabase assess --functions-dir ./supabase/functions
```

### 2.1 Assertions

- [ ] Command exits 0.
- [ ] Report file lands at `./supabase-migration-report-<UTC>.md`
      (default location; `--output -` to stdout also works).
- [ ] Report has sections in this order: **Tables → RLS policies →
      Auth users + providers → Storage → Edge functions →
      Postgres extensions**.
- [ ] Source URL in the header is **redacted** — password shows as
      `***`, not the real value.
- [ ] Every table you seeded appears with a row count.
- [ ] Auth users section shows the count of email + OAuth accounts.
- [ ] Storage section shows every bucket with `public/private`, an
      object count and total size; a multi-bucket project gets a ⚠
      noting Eurobase's one-bucket-per-project model.
- [ ] Functions section grades every local edge function: ✅ "runs as
      written with the Supabase compatibility layer" (with the service
      role, open calls and secrets named), or ⚠ with the number of calls
      that need a change and the first one's file and line. With no
      `--functions-dir` present it's a single ⚠ skip note.
- [ ] If you seeded a policy that reads `auth.jwt() -> 'app_metadata'`,
      it's graded ⚠ with a "needs review" note.
- [ ] If you have `pg_graphql` or another blocklisted extension
      installed, it appears in the Blockers section at the top.

### 2.2 Bug categories to watch for

- Report shape drifts (missing section, wrong order).
- Numeric formatting (100 rows shown as `100.0k`).
- Password leak in the source URL header.
- A Supabase-installed extension we don't grade at all (silent).

---

## 3. Step 2 — `schema` (writes a file; still no target contact)

**Goal:** translate DDL + RLS policies to a Eurobase migration file.

```bash
eurobase import supabase schema --output ./migrations/1000_from_supabase.sql
# or leave --output blank; default lands under ./migrations/<epoch>_from_supabase.sql
```

### 3.1 Assertions

`schema` dumps the `public` schema only (Supabase's `storage` schema is
not copied) and also reads the `anon` / `authenticated` privileges from
the source.

- [ ] If the source has access changes (GRANT / REVOKE restrictions,
      `current_user` / `current_setting('role')` / `request.jwt.*`
      checks, policies / triggers / views / defaults calling a former
      SECURITY DEFINER function, a policy or trigger that had to be
      left out), the command lists them, exits non-zero and writes
      **no file**. With `--accept-access-changes` it writes the file
      and lists them in its header.
- [ ] Otherwise exits 0. Migration file lands where expected.
- [ ] Stderr shows a "translator warnings" list only for genuine
      unknowns (e.g. `auth.jwt()`, custom-schema references) and for
      what was changed (SECURITY DEFINER removed, views switched to
      `security_invoker`, objects commented out).
- [ ] No `public.` qualifier is left except on the platform helpers
      (`public.is_service_role()`, `public.current_end_user_id()`),
      including in function bodies and `nextval('…'::regclass)`.
- [ ] Every `REFERENCES auth.users(id)` becomes `REFERENCES users(id)`.
- [ ] Every `auth.uid()` becomes `auth_uid()`, in function bodies too.
- [ ] Every `auth.role()` becomes a `CASE` that returns
      `'service_role'`, `'anon'` (no signed-in user) or `'authenticated'`.
- [ ] `TO anon` / `TO authenticated` / `TO service_role` are gone:
      each became an `auth_uid()` condition in the policy body
      (`NOT (…) OR (…)` for `AS RESTRICTIVE`).
- [ ] Every CREATE POLICY body is wrapped in
      `public.is_service_role() OR (…)`. Count the wraps: one per
      policy clause (USING + WITH CHECK count separately).
- [ ] A table with RLS on and no policies gets an
      `eurobase_service_role` policy at the end of the file.
- [ ] The preamble (`SET statement_timeout`, `SET search_path`,
      `set_config`, pg_dump 18's `\restrict` lines) is stripped; one
      `SET LOCAL check_function_bodies = false;` remains.
- [ ] `CREATE SCHEMA public`, `ALTER SCHEMA public OWNER TO`,
      `COMMENT ON SCHEMA public` — all stripped.
- [ ] No `SECURITY DEFINER` is left outside comments; views carry
      `security_invoker='true'`.
- [ ] `uuid_generate_v4()` in any spelling becomes `gen_random_uuid()`;
      `extensions.moddatetime(…)` triggers call `eurobase_moddatetime(…)`,
      defined at the top of the file.
- [ ] Extensions, Realtime publications, materialized views (and what
      depends on them) are commented out with a reason.
- [ ] Rerun idempotence: feed the output back through the translator;
      policies must NOT double-wrap
      (`public.is_service_role() OR (public.is_service_role() OR …`
      would be the regression).

Automated: `go test ./internal/cli/` (rule tests + a pg_dump 18 sample
through the server's validator) and, with `ASSESS_SCRATCH_DB_URL` set
to a throwaway Postgres 16, `TestSchemaImport_Fixture` (real pg_dump,
apply into a project-like schema, policies checked as a non-owner role).

### 3.2 Apply the file

```bash
# On the target Eurobase project:
eurobase migrations up
```

- [ ] `migrations up` completes without error.
- [ ] All tables land in the tenant schema (`tenant_<slug>.<table>`).
- [ ] Verify one policy from the console's SQL runner — should see the
      wrapped `USING (public.is_service_role() OR (…))` shape.

### 3.3 Bug categories

- A `public.` qualifier survives on a DDL keyword we forgot (JOIN,
  DROP TABLE IF EXISTS, ALTER TABLE IF EXISTS, INTO, FROM, ON, SEQUENCE,
  VIEW, TRIGGER, OWNED BY).
- Multi-line policy body not wrapped (pg_dump pretty-prints them).
- Dollar-quoted function body gets rewritten inside (should be
  byte-for-byte identical).
- E-string `E'a\'b'` false-closed at the `\'`.

---

## 4. Step 3 — `data` (users + rows)

**Goal:** move the users (`auth.users`, `auth.identities`) and the rows
of the public tables into the target project. Run it after the schema
migration from step 2 has been applied, against a project that has no
data yet.

```bash
eurobase import supabase data --migrations-dir ./migrations
eurobase migrations up
```

The command reads everything in one read-only snapshot and writes
nothing to Supabase. Together the files hold, in this order: users,
OAuth links, then each table's rows (parents before children) followed
by its sequence positions. Apply all of them.

To try it on part of the data first: `--exclude-table a,b` leaves those
tables' rows out; the command refuses when another table's rows refer to
one of them.

### 4.1 Assertions on emission

- [ ] Exits 0. One or more files land under `./migrations/`
      named `<epoch>_data_from_supabase_part_NNN.sql`.
- [ ] Each file is **under 512 KiB** (the migrations endpoint's cap).
- [ ] No file contains `BEGIN;` or `COMMIT;` (the endpoint refuses
      those).
- [ ] The `users` INSERT comes **before** any table rows, with ids
      **kept** from Supabase, emails in lower case and phone numbers
      with a leading `+`.
- [ ] `raw_user_meta_data` lands at the top level of `metadata`,
      `raw_app_meta_data` under `_app_metadata`; `display_name` /
      `avatar_url` are filled from the metadata when present.
- [ ] `user_identities` holds the OAuth links only (no `email` /
      `phone` rows), each with an `id`, under Eurobase's provider
      names: `azure` becomes `microsoft`, `linkedin_oidc` becomes
      `linkedin`. A Microsoft link is written with the object id
      (`oid`) from Supabase's `identity_data`, which is what Eurobase
      knows a Microsoft user by; without one the link is left out.
      Links for a provider Eurobase doesn't offer are left out too.
      Both are listed.
- [ ] Values are plain quoted literals without casts (`'1234.5678'`,
      `'NaN'`, `'{a,b}'`), `NULL` for nulls.
- [ ] A table with a `GENERATED ALWAYS AS IDENTITY` column is inserted
      with `OVERRIDING SYSTEM VALUE`.
- [ ] Each table with a serial / identity column is followed by a
      `SELECT setval(…)`.
- [ ] A table that has triggers gets `ALTER TABLE … DISABLE TRIGGER
      USER` before and `ENABLE TRIGGER USER` after each INSERT.
- [ ] A table that refers to itself and needs more than one INSERT:
      its self-referencing foreign key is dropped before the first
      INSERT and added back after the last one.
- [ ] The "Read before applying" list names what was changed or left
      out: deleted users, SSO users, users without email and phone,
      users without a password, passwords that can't be used (not
      bcrypt, or cost above 12), bans with an end date, OAuth links
      that were left out, tables whose triggers are switched off, rows
      too large for one file.

### 4.2 Assertions on apply

- [ ] `eurobase migrations up` applies every part in order without
      error.
- [ ] Every table has the same rows as the source. Compare with
      `SELECT count(*), md5(string_agg(t::text, ',' ORDER BY t::text))
      FROM <table> t` on both sides.
- [ ] Tables that a trigger writes to (audit logs) have the source's
      rows, not a second set; `updated_at` columns are unchanged.
- [ ] Inserting a new row into a table with a serial / identity column
      works (the sequence is past the imported ids).
- [ ] Triggers are enabled again; a trigger that was disabled on
      Supabase is still disabled.
- [ ] A user from Supabase can sign in on the target project with
      their **original password**.
- [ ] A user with an OAuth link: their next sign-in with that provider
      lands on the existing `users` row. The project must use the same
      OAuth app (client ID) as on Supabase. Check one sign-in per
      provider: only Google and GitHub ids have been tested, and
      whether a Microsoft link carries an `oid` depends on what
      Supabase stored.
- [ ] A user whose ban on Supabase had run out can sign in; a user
      with a running ban can't.
- [ ] An RLS policy from step 2 gates rows correctly — a user only
      sees their own data.

`TestDataImport_Fixture` (set `ASSESS_SCRATCH_DB_URL` to a throwaway
local database) runs emission, the server's validator, the apply and
the row-for-row comparison.

### 4.3 Bug categories

- A value that doesn't survive the copy (compare the md5 above):
  floats (`NaN`, `Infinity`), JSON `null`, `bit(n)`, arrays with
  quotes or NULLs, composite and domain types.
- Rows inserted before the users or the parent rows they reference
  (foreign-key error on apply).
- A foreign-key cycle between tables: a **loud error** on emit, not a
  wrong order. There is no automatic way around it: drop one of the
  foreign keys in the project, import the tables with `--table`, add
  it back.
- A source database with a day-first `DateStyle`: dates must still
  arrive unchanged.
- A table others inherit from: its rows must not arrive twice.
- A batch over 512 KiB on a wide-row table.
- Refresh tokens or sessions copied (they shouldn't be).

---

## 5. Step 4 — `storage` (copy the files)

**Goal:** copy every file of the source's buckets into the project, with
its owner. Run it after `data`, so the owners exist.

```bash
export SUPABASE_DB_URL='postgresql://…'
eurobase import supabase storage                 # report, changes nothing
export SUPABASE_URL='https://<ref>.supabase.co'
export SUPABASE_SERVICE_ROLE_KEY='…'
eurobase import supabase storage --apply         # copy
```

### 5.1 Assertions on the report

- [ ] Every bucket is listed with its file count, size and visibility,
      and the folder it becomes (`<bucket>/`).
- [ ] A public bucket says its files become developer files in a folder
      shared with everyone.
- [ ] A bucket whose size shows 0 with files in it has the note about
      `metadata->>'size'`.

### 5.2 Assertions on the copy

- [ ] Every file is in the project under `<bucket>/<name>`, byte for
      byte (compare a few with `sha256sum`).
- [ ] An end user's file: they can download it with their token; another
      user and an anonymous caller get 404.
- [ ] A public bucket's folder is listed under console → Storage →
      Sharing as public, and its files download without a token.
- [ ] Stopping the copy (Ctrl-C) and running it again copies only what
      was missing; a third run reports everything as already there.
- [ ] A file the project already had under the same name, or one
      changed in the project since the copy, is left as it is and listed
      under "Not copied".
- [ ] A public bucket whose folder already has files in the project, or
      is shared with signed-in users only, stops the copy before
      anything is shared.
- [ ] Files without `metadata.size` count as copied on the second run.
- [ ] A file whose owner isn't a user is a developer file
      (`--missing-owner skip` leaves it out).
- [ ] Sizes in console → Storage match the source.
- [ ] `.emptyFolderPlaceholder` files are not copied.

`storage_import` in `TestTeamEndToEnd` drives the import API with real
uploads to the harness's S3 server; `migrate_supabase_storage_copy_test.go`
covers the CLI's copy against fakes, and `TestListSourceFiles_Fixture`
(with `ASSESS_SCRATCH_DB_URL`) the listing of `storage.objects`.

### 5.3 Bug categories

- A file uploaded but not readable (no record, or recorded with size 0).
- A file with another owner than on Supabase, or readable by others.
- An existing file replaced by an import.
- Names with spaces, `#`, `?`, `%` or non-ASCII characters that arrive
  under another name.
- A large file (> 1 GB) that fails part-way, and whether the next run
  picks it up.

---

## 6. Step 5 — `functions` (run as written)

**Goal:** deploy each Supabase edge function to Eurobase without
rewriting it. `eurobase edge-functions deploy` builds a function written
for Supabase with a compatibility layer; `import supabase functions`
checks the whole folder first and can deploy it.

### 6.1 Preparation

```bash
# On your machine, in the Supabase project dir:
supabase functions download <fn-name>  # for each function you want to migrate
# now ./supabase/functions/<fn-name>/index.ts exists
```

### 6.2 Check

```bash
eurobase import supabase functions
# reads ./supabase/functions/* and ../config.toml, changes nothing
```

- [ ] Every function is listed as `✓ ready` or `✗ needs changes`.
- [ ] A function that calls `createClient(...)`, `Deno.serve(...)` or
      `serve(...)` and reads `Deno.env.get(...)` is **ready**.
- [ ] A function that uses something the layer doesn't have is listed
      with file and line and what to use instead: `functions.invoke()`,
      realtime channels, `select()` with an embedded table, `or()`,
      most `auth.admin` calls, sign-in calls, some storage calls,
      `Deno` file APIs, a direct database connection.
- [ ] A function whose imports can't be bundled (a Node-only library)
      shows the bundler's error.
- [ ] "service role" is shown for functions that read
      `SUPABASE_SERVICE_ROLE_KEY`; "no sign-in required" for functions
      with `verify_jwt = false` in `supabase/config.toml`.
- [ ] The secrets the functions read are listed once at the end.

### 6.3 Secrets

```bash
eurobase vault set STRIPE_SECRET_KEY <value>   # for each listed name
```

`Deno.env.get("NAME")` reads the function's own env vars first, then
the project's Vault.

### 6.4 Deploy + call

```bash
eurobase import supabase functions --deploy
# or one function:
eurobase edge-functions deploy <fn-name> --file supabase/functions/<fn-name>/index.ts --allow-service-role
```

- [ ] Functions that read the service role key are deployed with the
      allow-service-role setting; `eurobase edge-functions list` shows
      it.
- [ ] Called with a signed-in user's token, a function that checks
      `req.headers.get("Authorization")` and calls
      `auth.getUser(token)` gets that user.
- [ ] A client made with the service role key can write a table the
      user can't; a client made with the anon key is limited by RLS.
- [ ] A client made with a key the caller sends is not the service
      role (the service role key is a random value per request unless
      the project sets one).
- [ ] In a function open to calls without a signed-in user, an
      anon-key client's queries are refused when nobody is signed in;
      the service role key client works.
- [ ] `.eq("flag", "true")` on a boolean column, `update()` with an
      `undefined` value, and `.not("id", "in", "(1,2)")` behave as on
      Supabase.
- [ ] `insert`, `update`, `upsert`, `delete`, `select` with filters,
      `single()` / `maybeSingle()`, and `rpc()` return what the same
      call returned on Supabase (`{ data, error }`).
- [ ] A secret read at the top of a file (`new Stripe(Deno.env.get(
      "STRIPE_SECRET_KEY"))`) is there.

`function_supabase_compat` in `TestTeamEndToEnd` runs such a function
through the real runner and database.

### 6.5 Bug categories

- A value that arrives in the wrong type: arrays, jsonb, booleans,
  timestamps in `insert` / `update` / filters.
- A call the layer doesn't have that the check didn't flag (it then
  throws when the function runs, with the call's name).
- A `select()` string built from request input: a cast must be one type
  name, and nothing else may become SQL text.
- A function that trusts a caller because a header equals the service
  role key.
- A function that depends on something Supabase-specific outside the
  client: the project URL, `functions.invoke`, database webhooks.
- A bundle over the 1 MB code limit.
- A library that needs Node.js built-ins.

---

## 6b. Step 6 — `cron` (scheduled jobs)

**Goal:** carry the project's pg_cron jobs over to Eurobase cron jobs.

```bash
export SUPABASE_DB_URL='postgresql://…'
eurobase import supabase cron            # reads cron.job, changes nothing
eurobase import supabase cron --apply    # creates the ready jobs, switched off
```

### 6b.1 Assertions on the plan

- [ ] A project without pg_cron prints "No scheduled jobs" and exits 0.
- [ ] A SQL command is shown as a SQL job, without the `public.` prefix
      and without the trailing `;`.
- [ ] `net.http_post(url:='…/functions/v1/<name>', …)` is shown as a job
      that runs the function `<name>`; a fixed JSON `body` is its
      payload.
- [ ] No request header is carried into a job; their names are listed.
      No key (a `Bearer` token, a key in a URL's query string or in the
      body) appears in the output: the payload is shown by its keys.
- [ ] pg_net's positional form (`net.http_post(url, body, params,
      headers)`) is read like the named one.
- [ ] Schedules: `1-5`, `MON-FRI`, `8-18/2`, `*/3` on days or months and
      weekday `7` are written out as lists; a rewritten schedule is
      noted next to the job.
- [ ] Listed as "needs changes", with the reason: several statements;
      a pg_net call that isn't the whole command (in a CTE, a DO block,
      followed by another statement); Supabase's own schemas or another
      schema, quoted or not; the http extension; statements other than
      SELECT / INSERT / UPDATE / DELETE / CALL (VACUUM, TRUNCATE,
      REFRESH, ALTER, SET); a URL that isn't `/functions/v1/<name>`
      exactly, or is built in SQL; a schedule in seconds, `$`,
      `@reboot`.
- [ ] Day of month and weekday: `0 0 13 * 5` (neither field begins with
      `*`, so pg_cron runs it when either matches) is refused;
      `0 0 1-31 * 1` becomes every day; `0 0 */2 * 1` (begins with `*`,
      both must match) is carried over.
- [ ] A time zone that isn't an IANA name stops the command with a hint
      to pass `--timezone`.
- [ ] A job that is inactive on Supabase says so.

### 6b.2 Assertions on apply

- [ ] Every ready job exists in the project, **switched off**, with the
      source's time zone and run as the service role.
- [ ] Running it again leaves existing jobs as they are.
- [ ] `--enable` creates jobs switched on where they are active on
      Supabase.
- [ ] A function job whose function isn't deployed yet is created and
      says so.
- [ ] On a Free project the third job is refused with the plan's limit.

`TestTranslateCronSchedule_SameRunTimes` compares, minute by minute
over two years, when cron's rule and the scheduler's rule run each
translated schedule. `TestReadPgCronJobs_Fixture` (with
`ASSESS_SCRATCH_DB_URL`) reads a pg_cron-shaped table; `cron_import_from_supabase` in `TestTeamEndToEnd`
sends the import's requests to the real server.

---

## 7. End-to-end scenario (the whole story)

Run all five in order against the same pair of projects, on a Sunday
morning, with a Discord message logging every command. This is the
"can a tenant follow the readme without pinging us" test.

- [ ] `assess` → tenant reads the report, agrees to proceed.
- [ ] `schema` → migration file lands, tenant applies it via
      `migrations up`. Target schema matches source.
- [ ] `data` → parts land, tenant applies them via `migrations up`.
      Row counts match. A source user logs in on the target with
      their original password.
- [ ] `storage` → tenant runs the emitted `rclone` commands; object
      counts match; a sample file's bytes match.
- [ ] `functions` → each function's ported source deploys; the same
      HTTP request returns the same response as it did on Supabase.

Total elapsed time budget: **under 4 hours** for a project with 10
tables / 500 rows / 3 buckets / 100 MB / 3 functions. Anything over
that means we've missed a UX gap.

---

## 8. Regression pins (things that broke during review, must stay
fixed)

Sanity-check these against the specific inputs — they're the ones
we've already burned time on:

- [ ] `INSERT INTO events VALUES ('table public.orders was updated')`
      — the string literal survives byte-for-byte.
- [ ] `E'a\'b public.orders'` — the E-string escape doesn't false-
      close.
- [ ] Multi-line policy body pretty-printed by pg_dump — wrapped.
- [ ] `Deno.serve(handler, { port: 8000 })` — options warned; no
      dangling `, {…})`.
- [ ] `deno.json` with `main: "src/handler.ts"` — nested output path
      created, not a "no such file" error.
- [ ] `deno.json` with `main: "../etc/passwd"` — rejected, falls back
      to `index.ts`.

---

## 9. Sign-off criteria

Before we tell tenants the feature is ready:

1. Every checkbox in sections 2–7 passes on **one** real project.
2. Every regression pin in section 8 passes.
3. Full end-to-end scenario (section 7) completes in under 4 hours
   with **zero** manual patching outside the CLI's own outputs.
4. Any bug found gets a GitHub issue + a fix + a test that would have
   caught it. No known bugs at sign-off.

The beta-update paragraph in the next weekly email flips from "on
the way" to "ready to try" when all four are true.

---

## 10. Where to file bugs

- One issue per finding, tagged with the subcommand
  (`import-supabase-assess`, etc.).
- Reference this doc's section number so it's clear which check
  caught it.
- Include the source-side minimum reproducer (a stripped-down table,
  policy, or function) so we can pin a test.
- If the bug is silent-broken-output, mark the issue `ship-blocker`
  and it doesn't leave the queue until fixed + tested.
