-- 000110_pro_price_bump_25_and_noncommercial_free.down.sql
--
-- Reverts the €25 Pro price back to €19. Non-commercial framing
-- lives in Terms + copy, not schema — nothing to roll back on
-- that side.

BEGIN;

UPDATE public.plan_limits
   SET price_cents = 1900   -- €19.00 (prior)
 WHERE plan = 'pro';

COMMIT;
