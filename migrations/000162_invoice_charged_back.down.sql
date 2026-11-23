BEGIN;

UPDATE public.invoices SET status = 'paid' WHERE status = 'charged_back';

ALTER TABLE public.invoices DROP CONSTRAINT IF EXISTS invoices_status_check;
ALTER TABLE public.invoices
    ADD CONSTRAINT invoices_status_check
        CHECK (status IN ('pending', 'paid', 'failed', 'refunded'));

ALTER TABLE public.invoices DROP COLUMN IF EXISTS charged_back_at;

COMMIT;
