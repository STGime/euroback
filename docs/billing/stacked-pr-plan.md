# Stacked-PR plan — Mollie billing on Eurobase OÜ

Delivery plan for turning the Free/Pro/Team plan_limits scaffolding into a
working paid subscription flow. Written 2026-07-30, target first user
payment collected within 4 weeks.

## Context locks

- Estonian entity **Eurobase OÜ** (registry 17557586) formed 2026-07-22.
- Business bank account open, IBAN available for Mollie payouts.
- Mollie already in `sub_processors` (mig `000025`) — DPA impact is nil.
- Tables `subscriptions` + `invoices` exist since `000001` — need extra
  columns, no new tables at the core.
- Pro is **€19/mo per project**, Team **€149/mo per project** (Team stays
  "coming soon" — not in scope here).
- Estonian entity is below the €40k VAT threshold → **no VAT charged**.
  Invoices carry an Estonian VAT Act §19 non-liable notice.
- Currency: **EUR only**.
- Payment methods: SEPA-DD + Cards (Mollie mandate → recurring).
- Feature-flagged rollout: `BILLING_ENABLED=false` until PR 8, then flip.
- **No grandfathering.** 99% of beta users are on Free; the 1% on Pro
  get a login-time prompt with a **14-day grace** to add a payment
  method. After grace, auto-downgrade to Free.

## Design decisions

| Decision | Choice | Why |
|---|---|---|
| Mollie customer scope | Per `platform_user` | One user can own many projects |
| Subscription scope | Per project | Matches €19/mo per-project pricing |
| Payment methods | SEPA-DD + Cards | Both support Mollie mandates for recurring |
| Failed-charge grace | 7 days after Mollie's 3-strike retry | ~28 days from first failure to downgrade |
| Legacy-Pro grace | 14 days from `BILLING_ENABLED=true` | Enough to notice the mail + click through |
| Cancellation | End-of-period by default, immediate + partial refund on request | Standard SaaS shape |
| Invoicing | PDF on paid webhook → Scaleway Object Storage → signed URL | Below €40k means no accounting-package integration |
| Webhook idempotency | UNIQUE index on `invoices.mollie_payment_id` + upsert | Mollie only sends `{id}`, we call back for state — natural dedupe |
| Failure alerting | Failed webhooks > 5 in 10 min → Discord `#alerts` | Matches existing ops pattern |
| Founder pricing | **None** — public €19/mo for everyone | Removed on 2026-07-30 |

## The 8 PRs

Each PR follows the existing rule: one reviewable PR per feature with
docs. Open → reviewer → wait → fix → auto-merge → next.

### PR 1 — Mollie client + config plumbing
*~350 LOC, no user-visible change*

- `internal/billing/mollie/client.go` — thin REST client
- `internal/billing/mollie/types.go` — request/response structs (Customer, Subscription, Payment, Refund, Mandate)
- `internal/billing/mollie/errors.go` — typed errors
- `internal/billing/mollie/client_test.go` — table-driven tests against `httptest.Server` fake
- Secrets in `eurobase-secrets`: `MOLLIE_API_KEY_TEST`, `MOLLIE_API_KEY_LIVE`, `MOLLIE_ENV`
- CLAUDE.md updated with the Mollie secret block

**Ship gate:** unit tests green, no `cmd/` wiring, zero prod risk.

### PR 2 — Migration `000081`: extend `subscriptions` + new `billing_customers`
*~200 LOC SQL*

- `migrations/000081_billing_schema.{up,down}.sql`
- New table `public.billing_customers (platform_user_id UUID PK, mollie_customer_id TEXT UNIQUE, created_at TIMESTAMPTZ)`
- Extend `public.subscriptions` with: `mollie_customer_id`, `plan_code`, `price_cents`, `currency`, `billing_interval`, `started_at`, `next_charge_at`, `canceled_at`, `past_due_since`, `status` (CHECK IN `'incomplete','active','past_due','canceled','expired'`)
- Extend `public.invoices` with: `mollie_payment_id UNIQUE`, `pdf_object_key`, `paid_at`, `amount_cents`, `currency`
- Indexes: `idx_subscriptions_next_charge_at`, `idx_subscriptions_past_due_since`, `idx_invoices_mollie_payment_id`
- RLS: reuse existing project-scoped policies; add `scripts/verify-billing-rls.sh`

**Ship gate:** `pg_dump` diff reviewed, verify-billing-rls passes on clean DB.

### PR 3 — Checkout API
*~350 LOC + tests*

- `internal/billing/service.go` — `CreateCheckout(ctx, userID, projectID, planCode) → checkoutURL, error`
- Idempotent: existing active subscription → `ErrAlreadySubscribed`
- Lazy-create Mollie customer, create Mollie subscription with `sequenceType=first`, write `incomplete` row to `subscriptions`
- `internal/handlers/billing.go` — `POST /platform/billing/checkout {project_id, plan_code}` behind `BILLING_ENABLED`
- Auth: must be project owner
- Docs: `docs/billing/checkout-flow.md` (sequence diagram + curl script)
- `BILLING_ENABLED=false` in prod at merge, `true` in staging

**Ship gate:** manual QA against Mollie test mode, curl script in docs works.

### PR 4 — Webhook handler + state machine
*~550 LOC + tests*

- `internal/handlers/webhook_mollie.go` — `POST /platform/billing/webhook`
- Receives Mollie's `{id}`, calls back for canonical state, applies transition
- State machine (documented table):
  - `incomplete → active` (first payment paid)
  - `active → past_due` (charge failed, day 1)
  - `past_due → active` (retry succeeded)
  - `past_due → canceled` (3 strikes + 7d grace)
  - `active → canceled` (customer cancel end-of-period)
  - `any → expired` (Mollie subscription ended)
- On `active`: `projects.plan = subscription.plan_code`, write invoice row, enqueue PDF-render River job
- On `past_due` first-time: set `past_due_since = now()`, emit `usage_alerts` mail
- Idempotency: `INSERT ... ON CONFLICT (mollie_payment_id) DO NOTHING`
- Rate-limit: 60 req/min per source IP
- `internal/workers/billing_alerts.go` — Discord `#alerts` on >5 failed webhooks / 10min

**Ship gate:** every state transition has a passing test, manual round-trip works.

### PR 5 — Legacy-Pro grace + downgrade cron
*~300 LOC + tests*

- Migration `000082_legacy_pro_grace.{up,down}.sql`:
  - Adds `projects.legacy_pro_grace_until TIMESTAMPTZ NULL`
  - On migration: `UPDATE projects SET legacy_pro_grace_until = now() + interval '14 days' WHERE plan = 'pro'`
- `internal/billing/downgrade.go` — River `BillingDowngradeCheckJob`, hourly
  - Branch A: `subscriptions.status='past_due' AND past_due_since < now() - interval '7 days'` → downgrade
  - Branch B: `projects.plan='pro' AND no active subscription AND legacy_pro_grace_until < now()` → downgrade
- Downgrade action: `projects.plan='free'`, `subscriptions.status='expired'`, cancel Mollie subscription if present, emit `usage_alerts` mail
- Interaction with idle-pause: downgraded project's `last_active_at` starts fresh 30-day pause clock
- Tests: happy path, grace-not-elapsed, already-canceled edges

**Ship gate:** cron manually invoked in staging against seeded past-due sub AND seeded legacy-Pro sub — both downgrade correctly, both mails go out.

### PR 6 — Invoice PDF renderer + storage
*~400 LOC + tests*

- `internal/billing/invoice_pdf.go` — Go `html/template` → PDF (chromedp headless)
- Template pulls from `legalStrings`: Eurobase OÜ, Ahtri 12, registry 17557586, "Not VAT-registered under Estonian VAT Act §19"
- Line items: `<plan_code> subscription for project <name>, period <from>–<to>, €<amount>`
- `internal/workers/invoice_render.go` — River worker enqueued by PR 4 webhook on `active`
- Uploads to Scaleway Object Storage bucket `platform-invoices`, writes `invoices.pdf_object_key`
- `GET /platform/billing/invoices` — list for current user's projects
- `GET /platform/billing/invoices/:id/pdf` — 302 to signed URL (5 min TTL)
- Bucket created via CLI script (not migration — infra concern)
- Docs: `docs/billing/invoicing.md`

**Ship gate:** test webhook produces paid invoice, PDF opens in Preview readable.

### PR 7 — Console billing UI + legacy-Pro modal
*~700 LOC Svelte + tests*

- `console/src/routes/(app)/billing/+page.svelte` — org overview
- `console/src/routes/(app)/projects/[id]/billing/+page.svelte` — per-project
- `console/src/lib/billing.ts` — typed API wrappers
- **Legacy-Pro conversion modal** — in root `+layout.svelte`:
  - Detects `project.plan === 'pro' && !hasActiveSubscription`
  - Non-dismissable on grace day ≤3d; dismissable-per-session otherwise
  - Text: "Eurobase Pro is now paid — €19/mo per project. Your Pro project `<name>` is on a 14-day grace period until `<date>`. Add a payment method now, or switch to Free."
  - Two buttons: **"Add payment (€19/mo)"** → `/projects/{id}/billing?plan=pro`, **"Switch to Free"** → immediate downgrade
- Deep links: usage-alert emails, paused-project wake screen, `/pricing`
- Playwright E2E: checkout → mocked webhook → invoice PDF loop

**Ship gate:** manually walk the full flow in staging.

### PR 8 — Cancel + refund + prod flip
*~300 LOC + tests + rollout*

- `POST /platform/billing/subscriptions/:id/cancel {mode: "end_of_period"|"immediate"}`
- End-of-period: `canceled_at = now()`, project stays Pro until `next_charge_at`
- Immediate: Mollie cancel + prorated refund via Mollie API
- `internal/billing/refunds.go` — pro-ration calculator
- Console cancel modal with "keep Pro until `<date>`" or "cancel now (refund €`<x>`)"

**Prod rollout in the same PR:**
- `BILLING_ENABLED=true` in prod
- Cohort mail to 1% Pro beta users: "Pro on Eurobase — 14 days to add a card"
- Marketing site pricing page: "Get Pro" button live
- Discord `#launch` announcement

**Ship gate:** first real payment goes through (Stefan pays €0.01 test, then €19 sub as smoke test).

## Ordering + rough sizing

- **Week 1:** PR 1, PR 2, PR 3. Foundation, no user impact.
- **Week 2:** PR 4, PR 5. End-to-end plumbing green in staging.
- **Week 3:** PR 6, PR 7. Something reviewable by non-technical eyes.
- **Week 4:** PR 8. Live, first cohort mail out, first payment collected.

Realistic total: 4 weeks focused, 5 with the usual slippage.

## Pre-flight (do before PR 1 lands)

1. **Test-mode Mollie API key** — sign into `my.mollie.com` as Eurobase OÜ, get test key, drop into `eurobase-secrets` in staging.
2. **Bank account confirmed for payouts** — Mollie payouts land in Estonian IBAN.
3. **Estonian VAT non-liable wording** — accountant confirms the "not VAT-registered under §19" text for invoices.
