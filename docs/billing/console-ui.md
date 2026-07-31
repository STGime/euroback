# Billing console UI (PR 7)

Two new pages + one modal, all inside the `(app)` shell so the
platform-auth middleware guards them.

## Pages

### `/billing` — org overview

`console/src/routes/(app)/billing/+page.svelte`

Accountant's view. At-a-glance stats (paid this year, outstanding
invoice count, Pro-project count) + a table of every invoice
across every project the caller owns. Each row links to
`/p/{id}/billing` for the per-project actions.

PDF download uses `api.invoicePDFUrl(id)` — a URL string (not a
`fetch`) because the backend 302s to a presigned S3 URL and
`fetch` would trip CORS on the redirect. Rendered as
`<a target="_blank">`.

The 503 `billing_disabled` case renders a friendly banner rather
than an error — non-billing environments (staging pre-flip)
should read as "coming soon", not "broken".

### `/p/[id]/billing` — per-project

`console/src/routes/(app)/p/[id]/billing/+page.svelte`

Current plan, upgrade CTA, per-project invoice list.

**Auto-checkout deep link.** Arriving with `?plan=pro` and a
project on Free (or legacy-Pro pending payment) auto-triggers
`api.startBillingCheckout()` and redirects to Mollie. Used by:
- Legacy-Pro conversion modal's "Add payment" button.
- Usage-alert emails ("You're at 80% of Pro cap → /p/{id}/billing?plan=pro").
- Paused-project wake screen ("Upgrade to Pro").

**Success banner.** Mollie's redirect back after payment includes
`?status=success`; the page shows a green banner + a note that
activation may take a minute (async webhook + activation flow).

**Grace-day countdown.** When `legacy_pro_grace_until` is set, the
plan card shows "N days left to add a payment method"; red +
weighted at ≤3 days to match the modal's non-dismissable threshold.

## Legacy-Pro conversion modal

`console/src/lib/LegacyProModal.svelte`

Wired in `(app)/+layout.svelte`. On every app-shell mount, the
layout runs `api.listProjects()` and picks the first project
matching `plan='pro' && legacy_pro_grace_until != null`. If any
exist, the modal renders as a full-screen overlay.

**Dismissable per session** (sessionStorage keyed on project ID)
UNTIL grace ≤ 3 days, when it becomes non-dismissable. Rationale:
at ≤3 days the user is close to auto-downgrade and the modal is
the only reliable notification channel. Per-project dismiss so a
user with two legacy-Pro projects can silence one and still see
the other.

**Buttons:**
- **"Add payment (€19/mo)"** — routes to `/p/{id}/billing?plan=pro`
  which auto-triggers checkout.
- **"Switch to Free"** — routes to `/p/{id}/billing` (no auto-action).
  Deliberately NOT an immediate downgrade — a stray click on a
  dismissable modal shouldn't silently lose Pro status.
- **"Remind me later"** — only when dismissable; sets sessionStorage.

## Backend contract additions

- `Project.legacy_pro_grace_until` (new field, nullable). Also
  added to `internal/tenant/service.go` Project struct + JSON
  serialisation.
- `internal/billing/webhook.go` `activateFromFirstPayment` now
  clears `projects.legacy_pro_grace_until` alongside the plan
  flip, so a user who completes checkout falls out of the modal's
  detection rule immediately.

## Deep-link targets

The console is now the landing page for three deep links:

| Source | Target |
|---|---|
| Legacy-Pro modal "Add payment" | `/p/{id}/billing?plan=pro` |
| Usage-alert email "You're at 80% of Pro cap" | `/p/{id}/billing?plan=pro` (once PR 8 adds the mail) |
| Paused-project wake screen "Upgrade to Pro" | `/p/{id}/billing?plan=pro` |
| Invoice-ready mail "Download PDF invoice" | `/billing` (org overview) |
| Downgrade mail (PR 5) "billing page" | `/p/{id}/billing` |

All routes are inside the platform-auth middleware — anonymous
visitors get redirected to `/login`.

## Test coverage

Type-check clean (zero new svelte-check errors/warnings vs
baseline). No new unit tests in this PR — the console has no
established test infrastructure. Manual QA:

1. Create a Pro project via SQL:
   `INSERT INTO projects (owner_id, plan, legacy_pro_grace_until, ...)
    VALUES (..., 'pro', now() + interval '10 days', ...);`
2. Load the console → modal appears with "10 days left" (dismissable).
3. Set `legacy_pro_grace_until = now() + interval '2 days'` →
   modal becomes non-dismissable.
4. Click "Add payment" → redirected to
   `/p/{id}/billing?plan=pro` → auto-redirected to Mollie.
5. Complete Mollie test-mode payment (Paid outcome) → back to
   `/p/{id}/billing?status=success` → banner shows. Refresh:
   plan reads Pro, grace warning gone (webhook cleared the
   `legacy_pro_grace_until`), modal no longer appears.

## Follow-ups

- Cancel button (PR 8) — the "Switch to Free" flow is currently a
  navigation, not an action. PR 8 adds the immediate-cancel endpoint.
- The console lacks unit tests generally. If we want any coverage on
  the modal's dismiss + grace-day logic, adding Playwright would
  be the pattern; deferred as it's not billing-specific.
