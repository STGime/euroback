# Plan: Public beta launch — Estonia legals + tightened Free tier + onboarding drip

## Context

Company formation in Estonia closes next week. Once it does, we open Eurobase for public beta signups. Three blockers between "formation done" and "public beta open":

1. **Legal surface still says Berlin.** `TermsPage.vue`, `PrivacyPage.vue`, `ImpressumPage.vue` on the marketing site (`~/eurobase/src/pages/`) all name Stefan Gimeson at Postfach 37 03 29, 14133 Berlin, and Terms Section 13 pins governing law to Germany + Berlin courts. Beta-clause language is already in place (Section 3, "AS IS", no SLA, features may be incomplete) — that stays. The DPA at `~/euroback/docs/legal/v1/dpa.md` has `{{LEGAL_ENTITY}}` / `{{REGISTERED_ADDRESS}}` placeholders waiting to be filled. **Signup has no T&C acceptance checkbox** and there's no `legal_acceptances` table logging consent — flagged as "Phase 2" in the DPA doc. Opening public signups without both is a regulator risk.

2. **Free tier is too generous for a public audience.** The monetization proposal (`~/euroback/docs/monetization-proposal.md`) lays out four tightening moves — halve MAU / storage / bandwidth / realtime caps, add idle-pause after 30 days, gate custom-domain + BYO-SMTP + quota-alerts to Pro. Public beta is exactly when we want these live so first-time signups start with the tight numbers instead of grandfathering their way to profitability. Existing beta users grandfather at old limits for 90 days.

3. **The in-console onboarding wizard is solid, but nothing follows up out-of-app.** `console/src/routes/(app)/onboarding/+page.svelte` (695 lines) walks a new user through create-project → configure-auth → a success screen with quickstart / curl / IDE tabs and their public + secret keys. What's missing is anything *outside* the console: no welcome email in the inbox, no drip that keeps them engaged over the next 10 days as they explore Storage, Realtime, Functions, etc. `docs/emails/*.html` has our beta-update HTML shape we can clone. `internal/email/service.go:SendRaw()` is the send API; templates use Go `html/template`. **River is available for scheduled jobs** (`internal/workers/*` already has `ProvisionProjectWorker`, `TenantExportWorker`) — so scheduling six mails staggered over 10 days is a well-worn primitive. Also missing: opt-out (no `mailing_preferences` table), send-log for idempotency (no `drip_email_sends` table), and the templates themselves.

Fixing all three in the same launch window is the plan.

## Proposal — three phases, ships in ~2 weeks

### Phase A — Estonia legal pivot (must land BEFORE public beta opens)

Files to edit (all inline content, no CMS):

- `~/eurobase/src/pages/TermsPage.vue` — replace Berlin address with the Estonian entity's registered address; change Section 13 governing law to Estonia + Tallinn courts. Keep Section 3 (Early Access / beta clauses) as-is; strengthen the "we may pause / terminate / not preserve data" language with one added sentence about the beta window.
- `~/eurobase/src/pages/PrivacyPage.vue` — same address swap; add the Estonian entity as data controller. Sub-processor list already correct (Scaleway / Mollie / Google / GitHub with CLOUD Act flags) — no change.
- `~/eurobase/src/pages/ImpressumPage.vue` — swap address, VAT ID, registered court. Keep German language (the Impressum serves German-speaking visitors reading the marketing site; the entity is Estonian but the legal-info-page tradition is German).
- `~/euroback/docs/legal/v1/dpa.md` — fill `{{LEGAL_ENTITY}}` + `{{REGISTERED_ADDRESS}}` placeholders. Bump version to `v2` so anyone who accepted `v1` gets prompted to re-accept.
- `~/euroback/migrations/000075_dpa_v2_estonia.up.sql` — update `sub_processors.legal_entity` rows that reference the German entity; seed a `legal_documents` table with the v2 DPA + Terms + Privacy checksums.
- `~/euroback/migrations/000076_legal_acceptances.up.sql` — new table `legal_acceptances (user_id, document_type, document_version, accepted_at, ip, user_agent)`. FK to `platform_users`. Documents the DPA's own "Phase 2" placeholder.
- `~/euroback/console/src/routes/login/+page.svelte` — add T&C + DPA acceptance checkbox on signup (required to submit). Link labels go to `/legal/terms`, `/legal/privacy`, `/legal/dpa` on the marketing site.
- `~/euroback/internal/auth/platform_auth.go` — new arg on signup handler: `accepted_documents: []{type, version}`. Reject signup with 400 if T&C + DPA not both in the list. Write to `legal_acceptances` in the same transaction as `platform_users` insert.

Verification: sign up a fresh account without ticking the box → 400 "you must accept the Terms and DPA to continue"; tick it → signup succeeds AND a row lands in `legal_acceptances` with the correct document versions + client IP + UA.

### Phase B — Free-tier tightening (ships with Phase A, no separate deploy)

Everything from Phase A of `docs/monetization-proposal.md`. Concrete deltas:

- Migration `000077_free_tier_v2.up.sql` — update `plan_limits` `free` row: MAU 10 000 → 5 000, storage 1 024 → 512, bandwidth 5 120 → 2 048, realtime cxns 100 → 50. Add new columns `custom_domain BOOL`, `quota_alerts BOOL`, `byo_smtp BOOL` on the table; set Free = false, Pro = true.
- `internal/plans/limits.go` + `internal/plans/enforcement.go` — extend `PlanLimits` struct, add `CheckCustomDomain`, `CheckBYOSMTP`, `CheckQuotaAlerts`.
- `internal/plans/idle_pause.go` — new cron worker. Every hour, scans `projects` for `last_active_at < now() - interval '30 days' AND plan = 'free' AND state = 'active'`. Marks state → `paused`. Also new `projects.state` + `last_active_at` columns via `000078`.
- Wake-on-request middleware in `internal/gateway/router.go` — any authenticated request against a paused project short-circuits to 202 and synchronously flips `projects.state` back to `active` (a DB read + write, ~200 ms). Same middleware bumps `last_active_at` on every successful request. Because the shared Postgres cluster stays running, there is no cold-start work — the "pause" is only about the API + realtime + edge-function surface, not the DB.
- **Grandfather existing beta users** for 90 days: `projects.grandfathered_until TIMESTAMPTZ` column; enforcement functions consult the OLD `PlanLimits` values while `grandfathered_until > now()`. Set to `now() + interval '90 days'` for every existing free project in the same migration.
- Console updates: pricing page shows the new numbers; usage cards show the tighter caps; a small banner on grandfathered projects reading "your project's limits will change on <date> — see /pricing."

Verification: create a fresh free project → gets new caps immediately. Existing beta free project reads `grandfathered_until` → gets old caps until then. Fake `last_active_at = now() - 31 days` → cron worker flips state to `paused`. Hit the project URL → 202 on the first request, state flips to `active`, subsequent requests succeed normally.

### Phase C — Onboarding email drip (5-6 mails over 10 days)

Six mails, each every two days after signup. Same 600 px inline-styled HTML shape as the beta updates in `docs/emails/`. Every mail ends with an opt-out link.

Drip complements the wizard — it doesn't restart onboarding. Day 0 references the project the user just created; subsequent mails assume Auth is already wired and focus on features the wizard doesn't touch.

| Day | Subject | Focus |
|---|---|---|
| 0 | "Welcome to Eurobase (beta) — your project <name> is live" | Congratulates on the project + auth setup they just did in the wizard; links to the docs table of contents; explains the beta framing (as-is, may change, feedback loop is a reply to this mail) |
| 2 | "Row-Level Security in five minutes" | RLS preset patterns, the `is_service_role() OR (…)` policy shape, common mistakes. The wizard configured auth methods but didn't teach RLS — this is the first "you didn't know you needed this" mail. Docs chapter 2 |
| 4 | "Storage + Realtime: EU-hosted objects and live subscriptions" | S3-compatible buckets with signed URLs, WebSocket subs with row-filter. Docs chapters 4 + 5 |
| 6 | "Edge Functions + Vault: Deno handlers + AES-256 secrets" | Deploy a function via CLI, store an API key in the vault, invoke from the function. Docs chapters 6 + 12 |
| 8 | "CLI + MCP: your keyboard companion" | `eurobase` CLI install + `eurobase migrations up`, the MCP server for Claude Code / Cursor / Windsurf. Chapter 15 |
| 10 | "Compliance + what's next" | DSAR one-click, DPA report, audit log — features that only matter once you have real users. Then: what's on the roadmap (Supabase migration CLI in test, Team tier landing later). Soft nudge to Pro when the caps start biting. |

Infrastructure to build (all in `~/euroback`):

- **`migrations/000079_mailing_preferences.up.sql`** — table `mailing_preferences (user_id, category TEXT, opted_out_at TIMESTAMPTZ)`. Categories: `onboarding`, `beta_updates`, `usage_alerts`, `all`. Default all rows nonexistent → treated as opted-in.
- **`migrations/000080_drip_email_sends.up.sql`** — table `drip_email_sends (user_id, step INT, sent_at TIMESTAMPTZ, status TEXT, error TEXT)`. Idempotency guard: worker checks for a row before sending.
- **`internal/email/onboarding_templates.go`** — six Go `const` HTML template strings (or clone from `docs/emails/2026-07-06-beta-update.html` shape). Data struct `OnboardingData{UserEmail, DisplayName, Step, UnsubscribeURL, ProjectQuickstartURL}`.
- **`internal/workers/drip.go`** — new River worker `SendDripEmailJob{UserID, Step}`. Loads user, checks `mailing_preferences` (skip if opted out of `onboarding` or `all`), renders template, calls `emailService.SendRaw()`, writes row to `drip_email_sends`. Retries via River's built-in backoff.
- **`internal/workers/drip_enqueue.go`** — helper `EnqueueOnboardingSeries(ctx, tx, userID, signupTime)`. Called from the signup handler (Phase A already touches it). Inserts six River jobs with `ScheduledAt = signupTime + [0, 2, 4, 6, 8, 10] * 24h`.
- **`internal/handlers/unsubscribe.go`** — `GET /platform/mailing/unsubscribe?token=<HMAC(user_id + category)>`. Sets `mailing_preferences.opted_out_at = now()` for that (user, category). Renders a small confirmation HTML page with a link back to `/legal/privacy` and a "I opted out by accident — resubscribe" button.
- **Unsubscribe URL builder** in `internal/email/service.go`: `BuildUnsubscribeURL(userID, category)` → HMAC-signs `user_id|category|expires_at` with the existing platform HMAC secret, base64-encodes, returns absolute URL.
- Every onboarding template renders `{{.UnsubscribeURL}}` in its footer next to the sovereignty tagline. **Beta-update mails (already deployed) do not yet have this** — we should retrofit them in the same PR so all outbound platform mail carries an opt-out.

Optional but nice-to-have (defer if the timeline slips):
- **Send preview endpoint** — `POST /platform/mailing/preview` renders template N against the current user's data and returns HTML. Lets us QA drip content without spamming test inboxes.

Verification:
- Sign up a fresh test account (Phase A gates apply) → 6 rows appear in the River jobs table with `ScheduledAt` at day 0/2/4/6/8/10.
- Fast-forward day-0 job (River's admin CLI) → welcome email arrives; row lands in `drip_email_sends` with step=0, status=sent.
- Click the unsubscribe link in the day-0 mail → confirmation page shown; `mailing_preferences` row exists.
- Fast-forward the day-2 job → worker skips (query returns opted-out); logged in `drip_email_sends` as `status=skipped_opt_out`.
- Delete the test account → remaining River jobs get cancelled (`riverClient.JobCancel` in the account-delete handler).

## Ordering + rough sizing

- **Week 1 (formation-week + 3 days):** Phase A. Legal pages + acceptance table + T&C checkbox. **This has to land before the first public signup**, else we're collecting personal data without documented consent under an out-of-date jurisdiction.
- **Week 1 (days 4-7):** Phase B. Free-tier tightening + idle-pause. Grandfathering means no visible break for existing beta users; new signups start on the tight numbers.
- **Week 2:** Phase C. Onboarding drip + opt-out. Not launch-blocking (the mails can start firing a couple days after public open) but should be live within a week so the first cohort of public signups gets them.

## Files touched (summary index)

**Marketing site (`~/eurobase`):**
- `src/pages/TermsPage.vue`, `PrivacyPage.vue`, `ImpressumPage.vue`

**Backend (`~/euroback`):**
- `docs/legal/v1/dpa.md` → `docs/legal/v2/dpa.md` (versioned)
- `migrations/000075_dpa_v2_estonia.{up,down}.sql`
- `migrations/000076_legal_acceptances.{up,down}.sql`
- `migrations/000077_free_tier_v2.{up,down}.sql`
- `migrations/000078_project_state_last_active.{up,down}.sql`
- `migrations/000079_mailing_preferences.{up,down}.sql`
- `migrations/000080_drip_email_sends.{up,down}.sql`
- `internal/auth/platform_auth.go` — accept `accepted_documents` on signup + write acceptances + enqueue drip series
- `internal/plans/limits.go` — new fields, new defaults
- `internal/plans/enforcement.go` — three new `Check*` functions, grandfather branch
- `internal/plans/idle_pause.go` — NEW
- `internal/gateway/router.go` — wake-on-request middleware + `last_active_at` bump
- `internal/email/onboarding_templates.go` — NEW, 6 templates
- `internal/email/service.go` — `BuildUnsubscribeURL` helper + footer injection in `RenderTemplate`
- `internal/workers/drip.go` — NEW, River worker
- `internal/workers/drip_enqueue.go` — NEW, six-job enqueue helper
- `internal/handlers/unsubscribe.go` — NEW, opt-out endpoint

**Console (`~/euroback/console`):**
- `src/routes/login/+page.svelte` — T&C + DPA acceptance checkbox
- `src/routes/pricing/+page.svelte` — tighter Free-tier numbers, `grandfathered_until` banner logic
- `src/lib/api.ts` — extend `PlanLimits` type for new columns

## Decisions locked in

1. **Estonian entity strings — placeholders for now.** Formation completes next week; final name / address / VAT ID / registered-court aren't known yet. Ship Phase A with `{{LEGAL_ENTITY}}` / `{{REGISTERED_ADDRESS}}` / `{{VAT_ID}}` / `{{REGISTERED_COURT}}` placeholders wired through the `.vue` pages + DPA + migration seeds; a small `resolveLegalStrings()` helper in `~/eurobase/src/data/content.ts` returns the actual values from one config file. When formation completes, one commit fills the strings and the site rebuilds. The T&C acceptance checkbox + `legal_acceptances` table + governing-law swap to Estonia can all land ahead of the entity strings — they're not blocked on them.
2. **Team tier on the pricing page: yes, "Coming soon."** Add a third column on the console pricing table + on the marketing site's PricingSection with the €149 anchor + the three feature bundles (data guarantees / environments / team primitives / compliance). Every row shows a muted "Coming soon" tag the same way the Solution section's "Supabase Migration" card does today (`~/eurobase/src/components/sections/SolutionSection.vue`). No CTA button on the Team column — comment reads "planned for later this year."
3. **Grandfather window: 90 days.** Existing Free projects keep old caps for 90 days from the day migration `000077` lands. Console banner counts down; a one-off "your caps are changing on <date>" email 14 days before switchover.
4. **Drip mail sender: `hello@eurobase.app`.** Reply-to same. All six drip mails + beta-update mails going forward carry that From. Transactional mails (verification / reset / magic link) stay on their existing `noreply@` sender so DMARC pinning isn't disturbed.
5. **Paused-project wake includes an artificial ~30 s pause.** The real DB flip is ~200 ms — but the middleware deliberately sleeps ~28 s + jitter before returning 200 on the first request after a pause. That wait is the conversion lever: "Pro never pauses" needs to be a visible pain point on Free, not an invisible one. Console banner on the paused-then-woken page reads: "Your Free project was paused after 30 days idle. This first request took ~30 s to wake it — Pro projects never pause. [Upgrade →]" Implementation: `time.Sleep(28*time.Second + jitter)` inside the wake middleware, only on the state-flipping request; subsequent requests pass through instantly.
