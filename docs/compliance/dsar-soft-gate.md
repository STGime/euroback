# DSAR Console Soft-Gate

> #251 (part of #248). How the Compliance → Data Export console tab
> becomes a Pro-tier feature without blocking free-tier tenants from
> meeting their GDPR obligations.

## The contract

| Tier | Compliance → Data Export tab | API endpoints |
| ---- | ---------------------------- | ------------- |
| Free | "Upgrade to Pro" card        | callable      |
| Pro  | One-click export controls    | callable      |

The DSAR API endpoints
(`POST /platform/projects/{id}/compliance/exports`,
`POST /platform/projects/{id}/compliance/exports/user`,
`GET /platform/projects/{id}/compliance/exports`)
stay public to every tier. Only the polished console flow is gated.

## Why API stays public

DSAR is a **legal obligation** for the tenant — they are the data
controller for their end-users, and Article 12(3) of the GDPR puts a
one-month deadline on responses. A hard gate ("pay to comply with the
law") on the export endpoints would mean a free-tier project that hits
a real DSAR on its statutory deadline gets a payment wall instead of
a path to compliance. Bad legally, bad reputationally, bad framing
("Eurobase blocks GDPR compliance unless you pay").

The soft gate keeps the API path open. A determined free-tier admin
can script their own export against the same endpoints the Pro console
calls. The Pro tier saves them from doing that — but doesn't withhold
the capability.

## Where the gate lives

- **Schema**: `plan_limits.dsar_console_ui BOOLEAN` (migration 000072,
  default `false`). Backfill sets `free → false`, `pro → true`. A
  future tier added without an explicit value lands on `false` — the
  safer default (better to surprise-gate a new tier than surprise-
  ungate one).
- **Go**: `internal/plans/limits.go::PlanLimits.DSARConsoleUI`,
  surfaced via the existing `/platform/projects/{id}/usage` response.
- **Console**: `console/src/routes/(app)/p/[id]/compliance/+page.svelte`
  loads `usage.limits.dsar_console_ui` on mount and switches the
  Data Export tab body between the upgrade card and the existing
  export controls.

## Fail-open on lookup failure

If the usage lookup fails (Redis hiccup, network blip), the console
**defaults to enabled**. A paying customer must never be silently
locked out of running an export because the plan-resolver is flaky.
The API is the source of truth either way.

## What the audit log shows regardless of tier

Every export request, completion, and failure is recorded with the
actor's email + IP, via `audit.ActionExportRequested` /
`audit.ActionExportCompleted` / `audit.ActionExportFailed` — fired
from the handler and the worker respectively. The gate doesn't change
the audit shape; an export run via the API on a free tier produces
the same audit-log row as an export clicked through the Pro console.

## Operator runbook — upgrading a project

To flip a project from "console hidden" to "console visible":

1. Move the project to the `pro` plan (existing billing flow, no
   special endpoint for this gate).
2. The next page load on the Compliance → Data Export tab fetches
   the new limits and renders the export controls.

No cache invalidation needed beyond what the existing plan-tier
switch already triggers. The cache TTL on `LimitsService` is process
lifetime, so a deploy cycles it; pre-deploy, the project owner can
hit refresh after the plan switch to see the new state immediately
(the page calls `getUsage` on mount, not cached).

## Operator runbook — applying migration 000072 to an existing fleet

⚠ **One-time caveat caught in the #255 review.** `LimitsService`
caches `*PlanLimits` for process lifetime, keyed by plan name. A
gateway pod that warmed the `"pro"` cache entry BEFORE migration
000072 ran will keep returning a struct whose `DSARConsoleUI` is
the Go zero-value (`false`) — because the previous SQL didn't
include the new column. **Result: Pro tenants see the upgrade card
until the pod restarts.**

The normal CI flow is safe: the `migrate` Job in
`deploy/k8s/migrate-job.yaml` runs immediately before the gateway
Deployment rolls, so the cache is cycled by the same release.

**The danger window is a migration-only roll** — a manual
`migrate up`, a hotfix that touches only `migrations/`, or any
deploy where the gateway image SHA didn't change. In that case:

```bash
kubectl -n eurobase rollout restart deploy/gateway
```

after the migration has applied. The same caveat technically
applies to every `plan_limits` schema change going back to
migration 000026 (`edge_function_limit`); this one is just the
first where the cache miss produces a customer-visible regression
(a paying Pro customer who sees the upgrade card) rather than a
silent zero-default.

A future change to make `LimitsService` cache shorter-TTL or
listen for `LISTEN/NOTIFY` on `plan_limits` is the proper fix.
Tracked as a follow-up out of scope for #251.

## Related

- #248 — Umbrella: DSAR as Pro differentiator
- #249 — Tenant docs DSAR terminology (merged)
- #250 — Pricing page positioning (in review)
- #252 — Marketing page (separate `eurobase` repo)
