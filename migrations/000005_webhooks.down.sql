-- 000005_webhooks.down.sql
BEGIN;
DROP TABLE IF EXISTS public.webhook_deliveries;
DROP TABLE IF EXISTS public.webhooks;
COMMIT;
