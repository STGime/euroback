-- 000075_free_tier_tightening.up.sql
--
-- Phase B of the public-beta launch plan (docs/public-beta-launch-plan.md).
-- Tightens the Free tier for the public-beta audience and adds the three
-- new binary Pro-only gates.
--
-- Four cap changes on the `free` row of `plan_limits`. Each is either
-- halved or dropped by a well-considered increment to make the "you
-- have real users, upgrade" moment actually visible instead of
-- theoretical (see the monetization proposal for the reasoning):
--
--   Cap                      Was     Now     Why
--   MAU limit                10 000  5 000   clearest signal
--   Storage MB               1 024   512     matches Supabase Free
--   Bandwidth MB / month     5 120   2 048   real traffic hits it
--   Realtime cxns / project  100     50      realtime = Pro use case
--
-- Not changed: db_size_mb (already tight at 500), rate_limit_rps (100
-- is fine for prototypes), upload_size_mb (10 MB), log_retention_days
-- (1 day already differentiates Pro's 30).
--
-- Three new binary columns for Pro-only gates:
--
--   custom_domain     — CNAME your own domain to the project's REST +
--                       Auth surface. Not built yet but reserved so
--                       enforcement lands atomically when the feature
--                       does.
--   byo_smtp          — bring-your-own SMTP for auth mail. Wiring is
--                       in progress (#235); the gate lands here so
--                       whichever ships first doesn't need a second
--                       migration.
--   quota_alerts      — Slack / webhook alerts at 80 % of any quota.
--
-- Existing Free projects grandfather at the OLD caps for 90 days via
-- `projects.grandfathered_until` (migration 000076). The enforcement
-- layer consults that column and uses old-limit values while it's in
-- the future. This migration only touches the tier defaults for
-- FRESH signups.
--
-- Rollout note (same shape as #251 / migration 000072): plans.LimitsService
-- caches *PlanLimits values for process lifetime. Gateway pods that
-- warmed the "free" cache entry BEFORE this migration ran keep
-- returning the OLD numbers until they restart. CI runs the migrate
-- Job immediately before the gateway rolls, so the cache cycles on
-- the same change — same safety window as 000072.

BEGIN;

-- New binary Pro-only columns. Default false so any existing plan
-- row not touched by the UPDATE below stays gated (defensible zero
-- value).
ALTER TABLE public.plan_limits
    ADD COLUMN custom_domain BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN byo_smtp      BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN quota_alerts  BOOLEAN NOT NULL DEFAULT false;

-- Free tier: halve four caps.
UPDATE public.plan_limits
   SET mau_limit      = 5000,
       storage_mb     = 512,
       bandwidth_mb   = 2048,
       ws_connections = 50
 WHERE plan = 'free';

-- Pro tier: enable the three new binary gates.
UPDATE public.plan_limits
   SET custom_domain = true,
       byo_smtp      = true,
       quota_alerts  = true
 WHERE plan = 'pro';

COMMIT;
