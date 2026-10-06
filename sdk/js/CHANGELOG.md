# @eurobase/sdk — CHANGELOG

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## Unreleased

### Added — end-user passkeys (passwordless sign-in, #630)

`auth` gains WebAuthn passkey support — the same credential type as the console's login, now for your app's end users:

- `auth.signInWithPasskey()` — passwordless sign-in. No email needed: the browser lists the passkeys registered for your site and the user picks one (discoverable credential). On success it establishes a session exactly like `signIn()` (sets the session, emits `SIGNED_IN`).
- `auth.registerPasskey(nickname?)` — enrol a passkey on the **currently signed-in** user (requires a session, so a stolen session can't silently add an attacker credential).
- `auth.listPasskeys()` / `auth.deletePasskey(id)` — manage enrolled passkeys. Returns `Passkey` rows (`id`, `nickname`, `created_at`, `last_used_at`, `backed_up`).
- `auth.passkeysSupported()` — gate your UI; always `false` in Node / SSR.
- Named exports `passkeysSupported` and `passkeyErrorMessage` for building a custom flow against the raw ceremony.

Each method runs the full two-step ceremony (fetch options → `navigator.credentials.*` → post the result); browser cancellation / timeout surfaces as a friendly `error` string, never a throw. Passkeys must be enabled for the project (console → Auth → Passkeys) with a Relying Party ID matching your app's domain. Requires gateway #630 (endpoints under `/v1/auth/passkey/*`).

### Added — `ExportRequest.complete`

The type of `auth.exportMyData()` / `getMyExport()` results declares `complete` (the gateway already returns it): `false` when the archive is missing something. Gateway change (#665): for end users, `error` is now a generic message and the response no longer carries `warnings` or `s3_key` (never declared in this type) — the app's developer sees the details in the console.

## 0.8.1 — 2026-09-28

### Fixed — the session's user after an OAuth sign-in (#720)

`auth.handleOAuthCallback()` stored the session with an empty user (`id: ''`, `email: ''`) until the next token refresh, so `getSession().user.id` right after an OAuth sign-in broke inserts (`user_id` not a uuid) and storage keys. The session's `user.id` and `user.email` now come from the access token's claims (`sub`, `email`) straight away, and in the returned session, the persisted session and the `SIGNED_IN` event. A session stored by 0.8.0 after an OAuth sign-in is repaired when it's restored.

Like the rest of the client-side session, these values are unverified until the first server call (the gateway verifies every request) — don't base client-side authorization on them.

`auth.getUser()` now also replaces the stored session's user with the returned profile (display name, avatar, timestamps, …) when it's the same user.

No gateway change needed.

## 0.8.0 — 2026-09-27

### Added — `storage.getPublicUrl(key)`

A plain URL for a file in a folder the developer shared as **Public** (console → Storage → Sharing, gateway #697): `${url}/v1/storage/<key>?apikey=<public key>`, usable directly in `<img src>`. Throws if the client uses the secret key — a secret key must never end up in a URL.

### Changed — storage visibility (gateway, all plans)

These come from the gateway (#693, #697) and apply to every SDK version:

- `storage.list()` returns only files the caller may read — their own, plus developer files in folders shared with them — listed from the file index: `etag` is no longer included, `size` of files uploaded through a signed URL may be `0`, `last_modified` is the upload time.
- Developer files in shared folders can be downloaded and listed without signing in (public folders) or by any signed-in user; end users can't add or replace files in a shared folder, and their own files there stay private.
- Signed URLs requested with the public key expire after at most 1 hour.
- With the public key, `storage_objects` can't be written through `eb.db`, and its `uploaded_by` / `metadata` columns can't be read.

## 0.7.1 — 2026-09-24

### Fixed — multiple filters on the same column

`QueryBuilder` keyed its query parameters by column name, so a second filter on the same column silently replaced the first. A date range like

```ts
await eurobase.db.from('events')
  .gte('created_at', '2026-01-01')
  .lte('created_at', '2026-01-31')
```

sent only `created_at=lte.2026-01-31` and returned every row up to the end date. Filters are now collected as `[column, value]` pairs and sent as repeated query parameters (`created_at=gte.…&created_at=lte.…`), which the gateway ANDs together.

**Requires the matching gateway fix** (same release): the gateway previously read only the first value of a repeated parameter. Against an older gateway, 0.7.1 would apply only the *first* bound. `eurobase.app` is updated, so hosted projects are unaffected. The earlier workaround (a single filter plus client-side filtering, or raw SQL through an edge function) is no longer needed.

Gateway notes for direct REST callers: a repeated column parameter is now ANDed (`?status=eq.a&status=eq.b` used to apply only the first filter; PostgREST semantics). A read may carry at most 64 filters; more returns `400 too many filters (max 64)`.

### Fixed — Node processes no longer hang after sign-in

The automatic token-refresh timer kept the Node event loop alive, so a script that signed in (seed scripts, cron jobs, tests) didn't exit until the token was close to expiry — about an hour. The timer is now `unref()`'d in Node and Bun. Deno still keeps the process alive until the timer fires. Browsers and Workers are unaffected, and refresh still happens while the process is running for other reasons.

## 0.7.0 — 2026-08-27

### Added — TypeScript types for the Edge Function runtime

Customer-requested (MoltenCoffeeBean, Discord support 2026-08-27):

> The documentation provides some description of the API available for Edge functions. However, I think it would be good DX to have typescript types available for edge functions, too. Perhaps exported from the SDK?

Ships an authoritative type surface for `req`, `ctx`, and the handler shape as a **types-only subpath** at `@eurobase/sdk/functions`. Zero runtime code is emitted; consumers use a type-only import in their function source:

```ts
import type { EdgeHandler } from '@eurobase/sdk/functions'

const handler: EdgeHandler = async (req, ctx) => {
  const { orderId } = (await req.json()) as { orderId: string }
  const rows = await ctx.db.sql<{ id: string; total: number }>(
    'SELECT id, total FROM orders WHERE id = $1',
    [orderId],
  )
  return Response.json(rows[0])
}
export default handler
```

Types cover the full `ctx` surface — `ctx.db.sql`, `ctx.vault.get`, `ctx.storage.upload / createSignedUrl / delete`, `ctx.env`, `ctx.user`, `ctx.requestId`, `ctx.log.{info,warn,error}` — tracked 1:1 with the runner's `makeCtx()` in `functions-runner/worker_bootstrap.js`. Consumers get editor autocomplete + type-checking on every helper.

The subpath has **no `import` field** in `package.json` `exports`, only `types` — a consumer that tries to value-import from `@eurobase/sdk/functions` gets a clean bundler error instead of silently pulling in bytes, which reinforces that the module is for types only.

No breaking changes; the base `@eurobase/sdk` import surface is unchanged. Existing consumers can upgrade without touching any code.

## 0.6.0 — 2026-08-26

### Added — `service_only` RLS preset (#469)

- **New `rlsPreset: 'service_only'`** on `eb.db.schema.createTable()`. Locks a table down to the **secret (`ebsk_`) key only** — the public (`ebpk_`) key is denied every operation regardless of end-user auth state. Intended for tables your own backend touches via the service key exclusively (admin lookups migrated from another provider, PII the browser SDK must never reach, webhook-backing tables written by your Next.js/Express backend). Verified end-to-end against Postgres 16 on the standard non-owner runtime shape.
- Comprehensive doc comment on the `RLSPreset` union type mapping every preset's anon/end-user/secret contract in one place, and a new preset behaviour table in the README.

### Fixed — `read_only` preset now allows secret-key writes

- The `rlsPreset: 'read_only'` preset previously emitted only a `FOR SELECT USING (true)` policy, and Postgres RLS deny-by-default meant INSERT/UPDATE/DELETE were blocked for **everyone including the secret key**. The name implied "public reads, service writes" but the effect was "no one writes ever." Fixed to emit a companion `FOR ALL USING (public.is_service_role())` policy so the secret key can now INSERT/UPDATE/DELETE while anon-key reads still work.
- **Forward-only fix.** The preset applies at table-**creation** time, so any table you already created with the old `read_only` preset keeps the frozen-writes behaviour until it's recreated (or its policies are dropped and re-applied via the console DDL surface).

### Fixed — silent-failure at table creation now surfaces as HTTP 400

- Previously, if a requested `rlsPreset` failed to apply at CREATE TABLE time (e.g. the auto-detected user-id column didn't exist on your table), the response was a silent 200 with `rls_enabled: true` but no actual policies attached — indistinguishable from a working table until the first query came back with `"row-level security policy denied this operation"`. Now the request fails loudly (HTTP 400) with the underlying Postgres error, and the just-created table is dropped so retrying with a corrected preset is idempotent. This closes a class of "the preset silently broke the table" bug reports.

## 0.5.0 — 2026-07-03

### Added — end-user email flows (part of umbrella #257)

- **`eb.auth.verifyEmail(token)`** — confirms a user's email address using a token from the verification email. Call this from the page you configure as `email_verification_url`.
- **`eb.auth.forgotPassword(email, options?)`** — triggers a password-reset email. Always returns `error: null` (no enumeration).
- **`eb.auth.resetPassword(token, newPassword)`** — completes the reset using a token from the reset email.
- **`eb.auth.resendVerification(email, options?)`** — resends the verification email to an unconfirmed user.
- **`emailRedirectTo` option** on `signUp`, `requestMagicLink`, `forgotPassword`, `resendVerification`. Overrides the per-project `auth_config.{email_verification_url, password_reset_url, magic_link_url}` for a single call. Must be in `redirect_urls` allowlist — same defence Supabase runs on `additional_redirect_urls`.

### Fixed

- Backend (see PR #262) — verification / reset / magic-link emails now link to a **tenant-owned URL** instead of the broken `console.eurobase.app/verify-email` route that 404'd. Any tenant that had `require_email_confirmation` enabled had a broken signup; this release + the accompanying backend fix closes that. See tenant docs chapter 6 → "Email confirmation — end-to-end" (#261) for the setup guide.

## 0.4.0 — 2026-05-17

### Added

- **`eb.auth.exportMyData(format)`** and **`eb.auth.getMyExport(exportId)`** — self-serve DSAR exports for end-users (GDPR Article 15 / 20). The signed-in user queues an export of their own data and polls until ready. Rate-limited per user (default: 1 export / 24 hours).
- New types `ExportRequest` and `ExportStatus` re-exported from the package root.

### Notes

- Backend routes (`POST /v1/auth/me/export`, `GET /v1/auth/me/export/{id}`) have been live since the DSAR feature shipped — this release just adds the typed SDK helpers so apps don't need to hand-roll `fetch` + access-token plumbing for a "Download my data" button.

## 0.3.0 — 2026-05-12

### Added

- **`eb.functions.schedules`** — control-plane API for cron schedules attached to deployed edge functions. Closes the gap that forced developers to declare schedules through the dashboard separately from the function code (which lived in their repo).
  - `create(name, spec)` / `update(name, partialSpec)` / `get(name)` / `list()` / `delete(name)`
  - `createOrUpdate(name, spec)` for idempotent provisioning scripts
  - Discriminated `ScheduleError` with `code: 'already_exists' | 'not_found' | 'forbidden' | 'invalid'` so callers can branch on collisions cleanly.
- Schedules require a secret API key (`eb_sk_*`) — control-plane writes are gated server-side.
- Locked-in semantics: POSIX 5-field cron only, evaluated in the schedule's `timezone` (defaults to UTC); missed ticks during downtime dropped (no backfill); no overlap protection between ticks.

### Notes

- Function action_type extended to `function` on the backend so schedules can fire deployed edge functions directly (in addition to SQL and RPC actions).

## 0.2.3 — 2026-05-11

### Fixed

- **Realtime WebSocket URL** now passes `project_id` as a query parameter, fixing #62 where the gateway couldn't route events to the right project for end-user JWT realtime subscriptions.
- New `buildWebSocketURL()` helper on `RealtimeClient` so unit tests can assert the URL shape without spinning up a real socket.

## 0.2.2 — earlier

- Token propagation, realtime, edge function invocation, vault, DDL surface — see git history.

---

If you upgraded across multiple versions: the SDK is additive across 0.2 → 0.4. No breaking signature changes; the new methods are net-new namespaces on existing clients (`functions.schedules`, `auth.exportMyData`).
