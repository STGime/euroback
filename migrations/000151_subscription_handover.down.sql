DROP INDEX IF EXISTS public.idx_subscriptions_project_scheduled;

-- Scheduled rows can't survive the narrower CHECK.
UPDATE public.subscriptions
   SET status = 'canceled', canceled_at = COALESCE(canceled_at, now())
 WHERE status = 'scheduled';

ALTER TABLE public.subscriptions
    DROP COLUMN IF EXISTS predecessor_id,
    DROP COLUMN IF EXISTS starts_at;

ALTER TABLE public.subscriptions
    DROP CONSTRAINT IF EXISTS subscriptions_status_check;

ALTER TABLE public.subscriptions
    ADD CONSTRAINT subscriptions_status_check
        CHECK (status IN ('incomplete', 'active', 'past_due', 'canceled', 'expired'));
