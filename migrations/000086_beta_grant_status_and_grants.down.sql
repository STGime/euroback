-- 000086_beta_grant_status_and_grants.down.sql

BEGIN;

REVOKE DELETE ON public.project_databases FROM eurobase_gateway;

DROP INDEX IF EXISTS public.idx_subscriptions_project_live;
CREATE UNIQUE INDEX idx_subscriptions_project_live
    ON public.subscriptions(project_id)
    WHERE status IN ('incomplete', 'active', 'past_due');

ALTER TABLE public.subscriptions
    DROP CONSTRAINT IF EXISTS subscriptions_status_check;
ALTER TABLE public.subscriptions
    ADD CONSTRAINT subscriptions_status_check
        CHECK (status IN ('incomplete', 'active', 'past_due', 'canceled', 'expired'));

COMMIT;
