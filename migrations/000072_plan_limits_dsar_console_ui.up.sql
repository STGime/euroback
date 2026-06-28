-- 000072_plan_limits_dsar_console_ui.up.sql
--
-- #251 (part of #248): soft-gate the one-click DSAR console UI behind the
-- Pro tier. Free tier keeps the underlying API endpoints
-- (POST /platform/projects/{id}/compliance/exports + per-user) because
-- DSAR is a *legal obligation* for the tenant — a hard gate on the API
-- would mean "pay to comply with the law" for a free-tier project that
-- hits a statutory deadline, which is bad framing and bad UX.
--
-- This column drives only the **console render**: the Compliance →
-- Data Export tab shows the existing one-click flow when true, and an
-- "Upgrade to Pro" card when false. The API endpoints remain callable
-- on both tiers.
--
-- Defaults match the user-facing pricing-page split (#250):
--   free → false (API only; build your own export pipeline)
--   pro  → true  (one-click flow)
--
-- No explicit BEGIN/COMMIT: golang-migrate wraps each .up.sql in its own tx.

ALTER TABLE public.plan_limits
    ADD COLUMN dsar_console_ui BOOLEAN NOT NULL DEFAULT false;

COMMENT ON COLUMN public.plan_limits.dsar_console_ui IS
  '#251: gates the Compliance > Data Export console tab. NOT the API — the export endpoints stay callable on both tiers. See docs/compliance/dsar-soft-gate.md.';

-- Backfill the existing two rows. Free stays at the default (false);
-- Pro flips to true. Done explicitly here so a future row added without
-- a value will land on `false` (the safer default — better to surprise-
-- gate a new tier than to surprise-ungate one).
UPDATE public.plan_limits SET dsar_console_ui = true  WHERE plan = 'pro';
UPDATE public.plan_limits SET dsar_console_ui = false WHERE plan = 'free';
