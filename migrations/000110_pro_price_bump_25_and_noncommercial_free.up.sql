-- 000110_pro_price_bump_25_and_noncommercial_free.up.sql
--
-- Two related product changes bundled in a single migration:
--
-- 1. Pro price bump €19 → €25 per project per month.
--    Landing this cleanly is easy today because there are zero
--    paying Pro customers on production yet (billing went live
--    in migration 000108, Pro at €19; no subscriptions have
--    transitioned to active). The bump aligns Pro price roughly
--    with Supabase Pro's $25 headline, removing the "just chose
--    the cheaper one" reading of Eurobase's positioning; the
--    real differentiator (fixed caps vs surprise metered overages,
--    per-project not per-org billing, EU jurisdiction) becomes the
--    argument rather than the price.
--
-- 2. Explicit Free-tier non-commercial framing (no schema change —
--    the actual enforcement lives in Terms of Service). This
--    migration only bumps a documented convention: the Free tier
--    is for personal / educational / development use, and
--    commercial use requires Pro or higher. Terms page + console
--    project-create modal + marketing pricing card get the same
--    wording in the accompanying PR.
--
-- No new columns; the price change is a one-row UPDATE. Team +
-- Legal-Team prices unchanged (Team €149, Legal-Team still
-- closed-beta NULL).

BEGIN;

UPDATE public.plan_limits
   SET price_cents = 2500   -- €25.00
 WHERE plan = 'pro';

-- Sanity check: the update above must have hit exactly one row.
-- If it didn't (schema drift, plan renamed, migration order
-- wrong), fail loud rather than let a mismatched price silently
-- land in production. Uses a DO block so the ROLLBACK on ASSERT
-- fail rolls back this whole tx.
DO $$
DECLARE
    n INT;
BEGIN
    SELECT count(*) INTO n
      FROM public.plan_limits
     WHERE plan = 'pro' AND price_cents = 2500;
    IF n <> 1 THEN
        RAISE EXCEPTION 'expected exactly 1 pro row at 2500 cents; found %', n;
    END IF;
END $$;

COMMIT;
