# Plan: Pro-tier monetization + a Team tier for SMBs

## Context

Everyone stays on Free. Two things need to change:

1. **Free is too generous relative to the pain of not upgrading.** Numeric caps exist (10k MAU, 500 MB DB, 1 GB storage, 100 realtime cxns) but very few binary "Pro-only" features. Most users can build a real thing without ever touching the Pro tier. `internal/plans/limits.go` today gates only three things by feature: `CustomTemplates`, `DSARConsoleUI`, and higher webhook count. Everything else is a soft numeric cap the user doesn't feel until they suddenly do.

2. **We have no SMB / mid-market ceiling.** Someone whose project is generating real revenue has nowhere to go between €19/mo Pro and "call sales." A Team tier that offers separate DB, backups, dev/prod branches, SSO, RBAC, and an SLA fills that gap — and it's where the compliance-heavy EU customers Eurobase is best-positioned to win actually spend money.

Reference: Supabase runs Free ($0, 50k MAU but pauses after 7d idle), Pro ($25/mo, 7-day backups, $10 compute credit), Team ($599/mo, jump justified almost entirely by SOC2 + SSO + RBAC + audit logs + PITR), Enterprise (custom). Their Team-tier jump is 24×. Eurobase should be more accessible than that — but the *shape* of what Team offers (production guarantees + compliance + team primitives) is right.

Billing note: Mollie schema is in place (`subscriptions`, `invoices`, `mollie_customer_id` columns from migration `000001`), but there is **no handler code yet**. Any tier change is theoretical until that lands. This plan calls out where billing needs to slot in but isn't blocked on it — the free-side tightening + tier structure ships first, billing wiring second.

## Proposal

### Three-tier structure

| Tier | Price | Positioning |
|---|---|---|
| Free | €0/mo | Prototypes + learning. Pauses idle projects. |
| Pro | €19/mo | Solo devs shipping to real users. Unchanged price; more features. |
| **Team** (new) | €149/mo | SMBs running a business on it. Backups + dev/prod + SSO + RBAC + SLA. |
| Enterprise | Custom | Punt to Q4 — 99.99% SLA, CSM, on-prem option, security questionnaires. Not in this plan. |

€149 is deliberate: cheap enough that a bootstrapped 2-person team can afford it, expensive enough that the seat + backup + SSO features feel funded. Supabase Team at $599 leaves headroom. Anchor test at €149; A/B down to €99 if conversion is soft, up to €199 if we're leaving money on the table.

### Lever 1: Tighten Free (four moves)

1. **Idle-pause after 30 days.** No signed request in 30 days → project moves to `paused` state; next request wakes it (target ~5 s cold start). Milder than Supabase's 7-day pause. Saves Scaleway compute and signals "Free is for prototypes." **Pro & Team never pause.** Cron worker + `projects.state` column. Idle timer resets on any authenticated request or console visit.

2. **Halve four numeric caps** where the Free number is currently a comfortable ceiling rather than a real limit:

   | Cap | Now | Proposed | Rationale |
   |---|---|---|---|
   | MAU | 10k | **5k** | Hitting this is the clearest "you have real users, upgrade" signal. |
   | Storage | 1 GB | **500 MB** | Matches Supabase Free. |
   | Bandwidth | 5 GB/mo | **2 GB/mo** | Anyone with real traffic hits this. |
   | Realtime cxns | 100 | **50** | Real-time apps are a Pro-worthy use case. |

   Leave DB size (500 MB — already tight), RPS (100 — fine for prototypes), upload size (10 MB), and log retention (1 day) alone.

3. **New Pro-only binary gates.** Currently ungated features that Free users don't miss until they DO:

   - **Custom domain** (CNAME your own domain to the project's REST + Auth surface). Doesn't exist yet — build in Phase A.
   - **BYO SMTP** — currently in progress (#235). Gate to Pro when it ships.
   - **Slack / webhook alerts at 80% quota** — small feature, high perceived value.
   - **Team seats**: Free = solo, Pro = up to 3, Team = unlimited. Sharing project access with a coworker is currently ungated.

4. **Log retention already differentiates** (1 day Free vs 30 days Pro) — no change, but surface it more prominently in-app so Free users notice.

### Lever 2: New Team tier (€149/mo) — the SMB pitch

Everything Pro has, plus:

**Data guarantees (the "sleep at night" bundle):**
- **Dedicated database instance** — separate Postgres per project, not a shared cluster. Isolated compute; noisy-neighbour immunity.
- **Automatic daily backups** — 7-day retention. (Free + Pro share the pooled cluster; no per-project backups. Legal Team retains 30 days as part of the compliance premium.)
- **Point-in-time recovery** — 7-day PITR window. One-click restore from console.
- **1 restore per calendar month included** — snapshot-based OR PITR, either counts against the same cap. Additional restores are a support conversation while pricing is finalised; usage-based billing lands when Team crosses ~20 users.
- **Uptime SLA** — 99.9% with credits.

**Environments (the "we ship on Fridays" bundle):**
- **Dev / staging / prod branches** — one logical project, three environments. Share auth users + schema; isolate data. Cloneable via CLI.
- **Preview environments for functions** — every PR deploys a preview function URL. Auto-cleanup on merge/close.

**Team primitives (the "we're more than one person" bundle):**
- **SSO (SAML)** for console sign-in. The single biggest thing that makes a company willing to pay a large monthly fee.
- **RBAC** — Owner / Admin / Developer / Read-only roles at the project level.
- **Extended audit log** — 90 days (vs Pro 30, Free 4 h).
- **DSAR bulk export** — multi-user DSAR in one archive. Fits the "departing team member" and "HR request" use cases exactly.
- **Priority email support** — 24-hour SLA (vs best-effort on Pro).

**Compliance bundle (the "our customers ask us"):**
- **SOC 2 Type II** attestation shared under NDA — requires an external audit, calendar cost of ~4 months.
- **HIPAA-ready** roadmap + BAA — requires vendor DPA changes + operational work.
- **Extended DPA terms** — pre-negotiated liability caps, EU-only sub-processor commitment.

None of these should ever appear on Free — that's the point. Pro gets the first tier of the story (backups + basic email support); Team gets the production-grade version.

### Lever 3: No usage-based / overage billing (yet)

Supabase's overage model ($0.00325 per MAU, $0.125 per GB DB, etc.) works because they have a mature billing pipeline. Eurobase's Mollie integration isn't even wired yet. Hard caps + a clear upgrade path are simpler and don't scare people with unpredictable bills. Reconsider once tier-based Mollie subscriptions are live and running for 3 months.

### Lever 4: What we're deliberately NOT doing

- **No paywall on DSAR API.** Statutory legal obligation; free-tier users must be able to comply. Console flow stays Pro-only (already the case).
- **No Free-tier project deletion.** Idle-pause, not delete. A paused Scaleway compute is cheap; a lost customer relationship isn't.
- **No compute add-ons** (Supabase's Micro→16XL menu). Adds pricing complexity; wait for demand signal.
- **No SMS/MFA add-ons.** Bundle into Team tier when the enterprise MFA work happens.

## Migration order (rough sizing, not a hard plan)

### Phase A — Tier structure + gates (2-3 weeks)
- Migration `000075` (verify current head): add columns to `plan_limits`
  (`custom_domain`, `byo_smtp`, `team_seats`, `dev_branches`,
  `backups_days`, `pitr_hours`, `sso_saml`, `rbac_roles`, `sla_hours`,
  `bulk_dsar`), insert `team` row.
- `internal/plans/limits.go` — new struct fields + defaults + tier constants.
- `internal/plans/enforcement.go` — six new `Check*` functions.
- Idle-pause worker: cron + `projects.state` column + wake-on-request middleware.
- Free-tier cap changes go in the same migration; document as a breaking change.

### Phase B — Console UX (1-2 weeks)
- New "Team members" page (Pro+).
- Upgrade prompts at 80 % / 95 % cap on the dashboard's usage cards.
- "Backups" tab (visible Pro+, configurable Team+).
- Three-tier pricing table replaces the current two-column layout — both on `~/euroback/console/src/routes/pricing/+page.svelte` AND on the marketing site `~/eurobase/src/data/content.ts` + PricingSection.

### Phase C — Mollie wire-up (2-3 weeks, unblocks charging)
- Handler code in `internal/billing/` — subscription create/update/cancel + webhook.
- Console self-serve tier switcher (currently manual only).
- No overage — flat tier fee only.

### Phase D — Team-tier features (parallel-track, longer)
- Dedicated DB instance — Scaleway provisioning + connection routing.
- Backups + PITR — Scaleway RDB native scheduled backups (7d) + native PITR (7d window). No pg_dump path; retention set via `SetBackupSchedule` at provision time and reconciled hourly (see #459). On-demand snapshots removed (#461) — PITR-to-just-before covers the "before-migration" use case at zero storage cost.
- SSO SAML — likely gluu/keycloak or a hosted SaaS as identity broker.
- RBAC — role column on `project_members` + middleware check.
- SOC 2 audit — externally scheduled, ~4 months calendar.

## Verification

At the end of Phase A:
- Sign up a fresh Free account → default state is `active`.
- Simulate 31 days no activity → `projects.state = 'paused'`; hitting the project URL returns 202 + wakes in <10 s.
- Push a Free account past 5 k MAU → signup 429s with an upgrade CTA.
- Try to enable custom domain on Free → 402 with a `plan_required: pro` payload the console renders as an upgrade prompt.

At the end of Phase B:
- Console shows a three-tier pricing card grid on `/pricing`.
- A Pro-tier project shows the Team-members tab; Free doesn't.
- Marketing site's Solution section shows the Team-tier price + differentiators.

At the end of Phase C:
- Complete a Mollie flow: Free → Pro → downgrade → cancel.
- Subscription webhook writes correct rows to `subscriptions` + `invoices`.

At the end of Phase D:
- Team project shows a dev branch that shares auth but has isolated tables.
- Restore a Team project from a 3-day-old backup.
- Enroll a Team owner via SAML; verify RBAC blocks a Developer from editing plan settings.

## Files to add/modify (Phase A only — the concrete first step)

New:
- `migrations/000075_pricing_v2.up.sql` (+ `.down.sql`)
- `internal/plans/idle_pause.go` — cron worker
- `internal/billing/` — stub package for Phase C

Modify:
- `internal/plans/limits.go` — struct + tier constants
- `internal/plans/enforcement.go` — new `Check*` functions
- `internal/tenant/handler.go` — wake-on-request middleware for paused projects
- `console/src/routes/pricing/+page.svelte` — three-tier grid
- `console/src/lib/api.ts` — extended `PlanLimits` type
- `~/eurobase/src/data/content.ts` — Team tier on marketing site
- `~/eurobase/src/components/sections/PricingSection.vue` — three-tier layout

Confirmed reusable — do NOT reimplement:
- `internal/plans/limits.go:*` — `GetLimits(plan)` lookup pattern.
- `internal/plans/enforcement.go:*` — `Check*` naming + 402 response shape.
- `internal/ratelimit/limiter.go` — Redis sliding-window (already tier-aware).
- Mollie schema in migration `000001` — no schema change needed for billing wire-up.

## Open decisions (Phase A can start without these; needed by Phase B)

1. **Team price point.** €149 default; ship with that, watch conversion for 4 weeks, adjust.
2. **Idle-pause window.** 30 days proposed; Supabase does 7. Softer is more customer-friendly but costs more Scaleway compute.
3. **Is "custom domain" a Pro or Team feature?** Suggest Pro (single domain) + Team (multi-domain + wildcard), matching Supabase's tiered gate.
4. **Existing Free tenants over the new caps** — grandfather at old limits for 90 days, or hard cutover with a 30-day advance email? Recommend grandfather.
