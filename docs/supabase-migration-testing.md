# Supabase → Eurobase Migration — Test Plan

Umbrella: [#267]. Sub-issues: [#268] assess, [#269] schema, [#270] data,
[#271] storage, [#272] functions. All merged; **not** yet end-to-end
validated against a real Supabase project.

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
    Only `assess` uses it (storage + functions enumeration) and only
    read-only.

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
export SUPABASE_SERVICE_KEY='eyJ…'
eurobase import supabase assess
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
- [ ] Storage section shows every bucket with `public/private`.
- [ ] Functions section shows every edge function.
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

- [ ] Exits 0. Migration file lands where expected.
- [ ] Stderr shows a "translator warnings" list only for genuine
      unknowns (e.g. `auth.jwt()`, custom-schema references).
- [ ] Every `CREATE TABLE public.<t>` becomes `CREATE TABLE <t>` (no
      `public.` qualifier on the object).
- [ ] Every `REFERENCES auth.users(id)` becomes `REFERENCES users(id)`.
- [ ] Every `auth.uid()` becomes `auth_uid()`.
- [ ] Every `auth.role()` becomes the `CASE WHEN public.is_service_role() …`
      block.
- [ ] Every CREATE POLICY body is wrapped in
      `public.is_service_role() OR (…)`. Count the wraps: should be
      one per policy clause (USING + WITH CHECK count separately).
- [ ] The preamble `SET statement_timeout`, `SET client_encoding`,
      `SET search_path`, etc. are all **stripped**.
- [ ] `CREATE SCHEMA public`, `ALTER SCHEMA public OWNER TO`,
      `COMMENT ON SCHEMA public` — all stripped.
- [ ] `extensions.uuid_generate_v4()` becomes `public.uuid_generate_v4()`;
      `extensions.gen_random_uuid()` becomes `gen_random_uuid()`.
- [ ] Rerun idempotence: run `eurobase import supabase schema` a
      second time against the same input; policies must NOT double-
      wrap (`public.is_service_role() OR (public.is_service_role() OR …`
      would be the regression).

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

## 4. Step 3 — `data` (rows + auth translation)

**Goal:** move row data from public tables + `auth.users` +
`auth.identities` into the target project.

```bash
eurobase import supabase data --migrations-dir ./migrations
eurobase migrations up
```

### 4.1 Assertions on emission

- [ ] Exits 0. One or more files land under `./migrations/`
      named `<epoch>_data_from_supabase_part_NNN.sql`.
- [ ] Each file is **under 512 KiB** (the tenant-migrations endpoint
      cap). Check with `ls -la` — anything at or over is a bug.
- [ ] No file contains `BEGIN;` or `COMMIT;` (the endpoint rejects
      those).
- [ ] `INSERT INTO "users" …` uses **bare** table names, no
      `public.` / `tenant_<slug>.` prefix.
- [ ] Auth users show up in the `users` INSERT with UUIDs
      **preserved** from Supabase (same as `SELECT id FROM
      auth.users` on the source).
- [ ] OAuth-only users (no password) get a stderr note.
- [ ] `raw_user_meta_data` lands at the top level of `metadata`;
      `raw_app_meta_data` lands nested under `_app_metadata`.
- [ ] `auth.identities` rows land in `user_identities` with the
      original `user_id` preserved.

### 4.2 Assertions on apply

- [ ] `eurobase migrations up` applies every part in order without
      error.
- [ ] Row count per table on target matches source (`SELECT count(*)`
      on both).
- [ ] A user from Supabase can log in on the target project with
      their **original password** (bcrypt passthrough).
- [ ] A user with an OAuth identity: their next Google/GitHub sign-in
      links to the pre-existing `users` row (uses `user_identities`).
- [ ] An RLS policy from step 2 gates rows correctly — a user only
      sees their own data.

### 4.3 Bug categories

- A JSONB scalar (`"foo"`, `42`, `true`) fell through to a bare
  string literal without the `::jsonb` cast → apply-time failure.
- A `numeric` / `interval` / `inet` / range column emitted as a Go
  struct dump.
- Batch overshoots 512 KiB on a wide-row table.
- FK cycle detected: should be a **loud error** on emit, not a wrong
  order silently emitted.
- Refresh tokens migrated (they shouldn't be — the note says users
  re-authenticate).

---

## 5. Step 4 — `storage` (bucket plan + rclone)

**Goal:** produce a per-bucket rclone plan; execute it; verify bytes.

```bash
eurobase import supabase storage
# prints Markdown to stdout by default
eurobase import supabase storage --output ./storage-plan.md
```

### 5.1 Assertions on the plan

- [ ] Each source bucket appears with visibility, object count, total
      size, file-size limit, allowed MIME types.
- [ ] Each bucket has a single `rclone sync` command in the report.
- [ ] The command uses `--checksum` (Supabase mtimes are epoch, size+
      mtime comparison would re-copy everything on rerun).
- [ ] The command uses `sync`, not `copy`.
- [ ] Source is `supabase_src:<bucket>/`, destination is
      `eurobase_dst:<bucket>/` (trailing slash, no leading slash).

### 5.2 Execute the copy

For each printed command:

```bash
rclone sync --checksum --progress --transfers 8 \
  supabase_src:<bucket>/ \
  eurobase_dst:<bucket>/
```

- [ ] Command completes; object count on destination matches source
      (`rclone ls eurobase_dst:<bucket>/ | wc -l`).
- [ ] A sample file downloads with the same bytes on both sides
      (`rclone cat` + `sha256sum`).
- [ ] Rerun: nothing new copied (idempotence check — this is why
      `--checksum` matters).

### 5.3 Bug categories

- Bucket name with a dash / underscore mangled in the command.
- `--bucket <name>` filter with a bad name: the error must list the
  available buckets, not `available: ` (empty).
- Buckets with objects but zero total bytes on the report: harmless
  note about `metadata->>'size'` should appear.

---

## 6. Step 5 — `functions` (Deno handler port)

**Goal:** rewrite each Supabase edge function to Eurobase's runner
contract.

### 6.1 Preparation

```bash
# On your machine, in the Supabase project dir:
supabase functions download <fn-name>  # for each function you want to migrate
# now ./supabase/functions/<fn-name>/index.ts exists
```

### 6.2 Run the translator

```bash
eurobase import supabase functions
# reads ./supabase/functions/*
# writes ./eurobase/functions/*
```

### 6.3 Assertions on the output tree

- [ ] Each function has an `./eurobase/functions/<name>/index.ts`
      (or the entrypoint declared in per-function `deno.json`).
- [ ] `Deno.serve((req) => …)` and `serve((req) => …)` (bare form
      from the canonical Supabase template) both become
      `module.exports = async (req, ctx) => …`.
- [ ] `Deno.env.get('KEY')` becomes `ctx.env.KEY`.
- [ ] The `import { serve }` line from `deno.land/std/http/server.ts`
      is commented out.
- [ ] Functions that call `createClient(SUPABASE_URL, …)` inside the
      handler are **skipped** (not rewritten). The CLI prints the
      exact snippet to replace with `ctx.db.sql(…)`.
- [ ] A function with `return /pattern/.test(req.url)` (regex after
      `return`) survives byte-for-byte.
- [ ] A function with `// close ) here` comment inside the handler
      body survives.

### 6.4 Deploy + call

For each ported function:

```bash
# On the target Eurobase project:
eurobase functions deploy <fn-name> --dir ./eurobase/functions/<fn-name>
curl https://<project-id>.eurobase.app/functions/v1/<fn-name>
```

- [ ] Response matches what Supabase returned for the same input.
- [ ] Env-var reads (`ctx.env.KEY`) work if you set the same key on
      the Eurobase project's function env.

### 6.5 Bug categories

- Ported function fails to parse at deploy (`syntax error`).
- Ported function parses but returns 500 (wrong runner contract
  shape).
- A `serve(handler)` (bare) call missed because the walker's guard
  didn't detect the `deno.land/std/http` import.

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
