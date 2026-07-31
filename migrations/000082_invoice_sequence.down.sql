-- 000082_invoice_sequence.down.sql
--
-- Reverses 000082_invoice_sequence.up.sql.

BEGIN;

ALTER TABLE public.invoices
    DROP CONSTRAINT IF EXISTS invoices_invoice_number_key,
    ALTER COLUMN invoice_number DROP DEFAULT,
    DROP COLUMN IF EXISTS invoice_number,
    DROP COLUMN IF EXISTS invoice_mail_sent_at;

DROP SEQUENCE IF EXISTS public.invoice_number_seq;

COMMIT;
