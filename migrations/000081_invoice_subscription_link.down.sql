-- 000081_invoice_subscription_link.down.sql
--
-- Reverses 000081_invoice_subscription_link.up.sql.

BEGIN;

DROP INDEX IF EXISTS idx_invoices_subscription_id;

ALTER TABLE public.invoices
    DROP COLUMN IF EXISTS subscription_id;

COMMIT;
