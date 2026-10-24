-- 000146_webhook_deliveries_event.up.sql
--
-- #845: customer webhooks are delivered (internal/workers,
-- DeliverProjectWebhookWorker). Each event keeps one row per webhook,
-- updated on every retry, so the console's delivery list shows one line
-- per event with its latest status and attempt count. Rows written
-- before this migration have no event_id and are left alone.

ALTER TABLE public.webhook_deliveries
    ADD COLUMN IF NOT EXISTS event_id   UUID,
    ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ;

CREATE UNIQUE INDEX IF NOT EXISTS idx_webhook_deliveries_webhook_event
    ON public.webhook_deliveries (webhook_id, event_id)
    WHERE event_id IS NOT NULL;
