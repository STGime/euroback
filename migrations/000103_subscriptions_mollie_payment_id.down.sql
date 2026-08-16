-- 000103_subscriptions_mollie_payment_id.down.sql

BEGIN;

DROP INDEX IF EXISTS public.idx_subscriptions_mollie_payment;
ALTER TABLE public.subscriptions DROP COLUMN IF EXISTS mollie_payment_id;

COMMIT;
