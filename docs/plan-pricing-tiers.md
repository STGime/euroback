# Eurobase Pricing Plan — Implementation Plan

## Competitive Analysis Summary

| | Supabase Free | Supabase Pro | Eurobase Free | Eurobase Pro |
|--|--|--|--|--|
| **Price** | $0 | $25/mo | $0 | **$19/mo** |
| **Database** | 500 MB | 8 GB | 500 MB | 5 GB |
| **Storage** | 1 GB | 100 GB | 1 GB | 50 GB |
| **Bandwidth** | 5 GB | 250 GB | 5 GB | 100 GB |
| **Auth users** | 50K MAU | 100K MAU | 10K MAU | 100K MAU |
| **Projects** | 2 | Unlimited | 2 | 10 |
| **Auto-pause** | Yes (7 days) | No | No | No |
| **EU sovereign** | No | No | Yes | Yes |

Pricing rationale: $19/mo undercuts Supabase Pro ($25) while emphasizing the sovereignty premium. No auto-pause on free tier is a strong differentiator vs Supabase.

## Free Tier ($0/month)

Goal: Let developers build and ship without paying. Generous enough to keep side projects live.

| Limit | Value |
|-------|-------|
| Projects | 2 |
| Database size | 500 MB per project |
| File storage | 1 GB per project |
| Bandwidth (egress) | 5 GB/month |
| Auth end-users | 10,000 MAU |
| API rate limit | 100 req/s |
| Realtime connections | 100 per project |
| File upload size | 10 MB |
| Webhooks | 3 per project |
| Request log retention | 1 day |
| Email templates | Defaults only (no custom) |
| Support | Community (GitHub Issues) |

Not included in Free:
- Custom email templates
- Custom domains (future)
- Team members (future)
- Point-in-time recovery (future)

## Billing Model

Per-project pricing (like Supabase). Each project has its own plan and is billed independently.
Users can mix Free and Pro projects. Example scenarios:

| Scenario | Monthly cost |
|----------|-------------|
| 1 free project | $0 |
| 2 free projects | $0 |
| 1 free + 1 pro | $19 |
| 2 pro projects | $38 |
| 3 pro + 2 free | $57 |

Project limits: Free users can have up to 2 projects. Users with any Pro project
can have up to 10 projects total (mix of free and pro).

## Pro Tier ($19/month per project)

Goal: Production-ready for B2B SaaS. Everything a growing startup needs.

| Limit | Value |
|-------|-------|
| Projects | 10 |
| Database size | 5 GB per project |
| File storage | 50 GB per project |
| Bandwidth (egress) | 100 GB/month |
| Auth end-users | 100,000 MAU |
| API rate limit | 1,000 req/s |
| Realtime connections | 10,000 per project |
| File upload size | 50 MB |
| Webhooks | Unlimited |
| Request log retention | 30 days |
| Email templates | Full customization |
| Support | Email (priority response) |

## Implementation Phases

### Phase 1: Plan Enforcement Backend

| File | Action | Description |
|------|--------|-------------|
| `migrations/000017_plan_limits.up.sql` | Create | `plan_limits` table with all limits per plan; seed free/pro rows |
| `migrations/000017_plan_limits.down.sql` | Create | Reverse |
| `internal/plans/limits.go` | Create | `PlanLimits` struct, `GetLimits(plan)`, cached lookup from DB |
| `internal/plans/enforcement.go` | Create | `CheckDatabaseQuota`, `CheckStorageQuota`, `CheckMAU`, `CheckWebhookLimit` |
| `internal/ratelimit/middleware.go` | Modify | Look up actual project plan instead of hardcoded "free" |
| `internal/storage/handler.go` | Modify | Per-plan upload size limit; check storage quota before upload |
| `internal/enduser/auth_service.go` | Modify | Check MAU limit on signup |
| `internal/webhook/handler.go` | Modify | Check webhook count limit on create |
| `internal/email/handler.go` | Modify | Gate custom templates behind pro plan |
| `internal/gateway/router.go` | Modify | Wire plan limits into handlers |
| `cmd/gateway/main.go` | Modify | Initialize plan limits service |

### Phase 2: Usage Tracking

| File | Action | Description |
|------|--------|-------------|
| `internal/plans/usage.go` | Create | `GetUsage(projectID)` — queries current DB size, storage size, MAU count, bandwidth |
| `internal/plans/handler.go` | Create | `GET /platform/projects/{id}/usage` — returns current usage vs limits |
| `internal/gateway/router.go` | Modify | Mount usage endpoint |

### Phase 3: Console UI — Plan & Usage

| File | Action | Description |
|------|--------|-------------|
| `console/src/lib/api.ts` | Modify | Add `getUsage(projectId)`, `getPlans()` methods |
| `console/src/routes/(app)/p/[id]/+page.svelte` | Modify | Usage bars on overview (DB size, storage, MAU vs limit) |
| `console/src/routes/(app)/p/[id]/settings/+page.svelte` | Modify | Plan display, upgrade button, usage breakdown |
| `console/src/routes/(app)/pricing/+page.svelte` | Create | Plan comparison page with upgrade flow |

### Phase 4: Mollie Billing Integration

| File | Action | Description |
|------|--------|-------------|
| `internal/billing/mollie.go` | Create | Mollie API client (create customer, create subscription, handle webhooks) |
| `internal/billing/handler.go` | Create | `POST /platform/billing/checkout`, `POST /platform/billing/webhook` |
| `internal/billing/service.go` | Create | Upgrade/downgrade logic, subscription management |
| `internal/gateway/router.go` | Modify | Mount billing routes |
| `console/src/routes/(app)/account/+page.svelte` | Modify | Billing section — current plan, payment method, invoices |

### Phase 5: Upgrade Prompts & Guardrails

| File | Action | Description |
|------|--------|-------------|
| Console (various pages) | Modify | "Upgrade to Pro" banners when approaching limits (80%+) |
| `internal/plans/enforcement.go` | Modify | Soft limits (warning at 80%) vs hard limits (block at 100%) |
| Email notifications | Create | "You've used 80% of your database quota" automated emails |

## Database Schema: plan_limits table

```sql
CREATE TABLE public.plan_limits (
  plan            TEXT PRIMARY KEY,
  db_size_mb      INT NOT NULL,
  storage_mb      INT NOT NULL,
  bandwidth_mb    INT NOT NULL,
  mau_limit       INT NOT NULL,
  rate_limit_rps  INT NOT NULL,
  ws_connections  INT NOT NULL,
  upload_size_mb  INT NOT NULL,
  webhook_limit   INT NOT NULL,     -- 0 = unlimited
  project_limit   INT NOT NULL,
  log_retention_days INT NOT NULL,
  custom_templates BOOLEAN NOT NULL
);

INSERT INTO plan_limits VALUES
  ('free', 500, 1024, 5120, 10000, 100, 100, 10, 3, 2, 1, false),
  ('pro',  5120, 51200, 102400, 100000, 1000, 10000, 50, 0, 10, 30, true);
```

## Priority Order

1. Phase 1 (plan enforcement) — must ship before any paid users
2. Phase 2 (usage tracking) — needed so users can see where they stand
3. Phase 3 (console UI) — makes limits visible and upgrade actionable
4. Phase 4 (Mollie) — enables actual payments
5. Phase 5 (upgrade prompts) — conversion optimization

Phases 1-3 can be built without Mollie. Pro can launch with manual upgrades
(user emails, plan flipped in DB) while Mollie integration is built.
