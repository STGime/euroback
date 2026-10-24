DROP INDEX IF EXISTS public.idx_webhook_deliveries_webhook_event;
ALTER TABLE public.webhook_deliveries
    DROP COLUMN IF EXISTS updated_at,
    DROP COLUMN IF EXISTS event_id;
