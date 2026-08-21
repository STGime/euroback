-- 000106_billing_profiles.down.sql
--
-- Rollback: drop the trigger, function, and table. Grants
-- disappear with the table.

BEGIN;

DROP TRIGGER IF EXISTS trg_billing_profiles_touch_updated_at
    ON public.billing_profiles;

DROP FUNCTION IF EXISTS public.billing_profiles_touch_updated_at();

DROP TABLE IF EXISTS public.billing_profiles;

COMMIT;
