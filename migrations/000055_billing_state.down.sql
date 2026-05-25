-- 000055_billing_state.down.sql

DROP INDEX IF EXISTS public.idx_subscriptions_pending_cancel;
DROP INDEX IF EXISTS public.idx_subscriptions_grace_active;

ALTER TABLE public.subscriptions
    DROP COLUMN IF EXISTS grace_until,
    DROP COLUMN IF EXISTS cancel_at_period_end;
