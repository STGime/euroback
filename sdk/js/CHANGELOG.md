# @eurobase/sdk — CHANGELOG

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
