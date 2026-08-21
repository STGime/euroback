-- 000105_projects_comped.up.sql
--
-- Adds a per-project "comp" (complimentary Pro) window. Distinct
-- from legacy_pro_grace_until (000080):
--
--   * legacy_pro_grace_until — "you have 14 days to add a card or
--     you drop to Free" (grace clock).
--   * comped_until          — "we're giving you Pro for free until
--     this date; no card required."
--
-- Motivation: pre-launch beta users tested the checkout flow before
-- BILLING_ENABLED=true landed in prod (Mollie was in test-mode).
-- Test-mode mandates do not roll into live-mode — separate customer
-- namespaces — so a "migrate the sub" path doesn't exist. Rather
-- than force these ~2 users to re-checkout under duress, we
-- grandfather them for 12 months as founder-tier comps.
--
-- Design:
--
--  * Nullable column. NULL = no comp. Presence + future timestamp
--    = comp active. Past timestamp = comp lapsed (row falls back
--    to the legacy_pro_grace_until path).
--
--  * comped_reason is free text so future operators can bisect
--    "why is project X on Pro without a subscription?" without
--    SQL forensics or CRM archaeology.
--
--  * The downgrade cron's Branch B (findLegacyProGraceElapsed in
--    internal/billing/downgrade.go) gains one extra WHERE clause:
--        AND (p.comped_until IS NULL OR p.comped_until < now())
--    so comped rows are skipped until comp expires.
--
--  * Backfill is a separate step. This migration only ships the
--    column + index; the two beta-tester UUIDs are applied via
--    scripts/comp-beta-pro-projects.sql (idempotent, prompts for
--    confirmation) — kept out of the migration so the UUIDs
--    aren't committed to the public repo.
--
--  * NOT a general "gift Pro" superadmin affordance yet. Policy
--    is capped at the current pre-launch cohort. A future console
--    UI + audit-log entry can wield this column safely; until
--    then, comp is a superadmin-only manual UPDATE.

BEGIN;

ALTER TABLE public.projects
    ADD COLUMN IF NOT EXISTS comped_until  TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS comped_reason TEXT;

-- Partial index — same shape as idx_projects_legacy_pro_grace_until.
-- Downgrade cron scans this column hourly; 99 %+ of rows will have
-- NULL here, so a partial index avoids wasting space + slowing
-- unrelated UPDATEs.
CREATE INDEX IF NOT EXISTS idx_projects_comped_until
    ON public.projects (comped_until)
    WHERE comped_until IS NOT NULL;

COMMIT;
