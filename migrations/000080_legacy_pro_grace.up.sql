-- 000080_legacy_pro_grace.up.sql
--
-- PR 5 of the billing stack (docs/billing/stacked-pr-plan.md).
-- Adds a per-project grace window so existing beta-Pro users get
-- 14 days from BILLING_ENABLED=true (PR 8) to add a payment
-- method before their project is auto-downgraded to Free.
--
-- Design:
--
--  * Column is nullable. NULL = "no grace applies" (i.e. either a
--    Free project, or a Pro project that already has a paid
--    subscription). NOT NULL = "Pro without an active subscription;
--    downgrade at the timestamp".
--
--  * Backfill sets the deadline to now() + 14d for every existing
--    Pro project. That's exactly right for the 1 % of beta users
--    currently on Pro: they get 14 days from the moment this
--    migration lands to convert. New Pro projects (post-billing-
--    flip) never get this column set — checkout writes a
--    subscription row directly, and the downgrade query's
--    "no live subscription" guard means it never fires on them.
--
--  * We deliberately do NOT set the grace during the checkout
--    flow. Only the migration + a manual "gift Pro without
--    payment" superadmin path (deferred) touch this column.
--
--  * The 14-day window is measured from migration-apply time, not
--    from BILLING_ENABLED flip time. If those are more than a few
--    days apart (they shouldn't be — PR 8 runs the migration in
--    prod and flips the flag same-day), an operator can extend
--    grace by hand via SQL. Documented in the runbook.

BEGIN;

ALTER TABLE public.projects
    ADD COLUMN IF NOT EXISTS legacy_pro_grace_until TIMESTAMPTZ;

-- Backfill: every existing Pro project without an active
-- subscription gets a 14-day countdown. The subquery is defensive
-- — normally there are zero paid subscriptions at this migration
-- point, but if PR 3's checkout wired somebody up already we don't
-- want to double-downgrade them.
UPDATE public.projects
   SET legacy_pro_grace_until = now() + interval '14 days'
 WHERE plan = 'pro'
   AND id NOT IN (
        SELECT project_id
          FROM public.subscriptions
         WHERE status IN ('incomplete', 'active', 'past_due')
   );

-- Index for the hourly downgrade scan. Partial index because
-- 99 %+ of rows have NULL here — a full-column index would waste
-- space and slow every UPDATE that doesn't touch this column.
CREATE INDEX IF NOT EXISTS idx_projects_legacy_pro_grace_until
    ON public.projects (legacy_pro_grace_until)
    WHERE legacy_pro_grace_until IS NOT NULL;

COMMIT;
