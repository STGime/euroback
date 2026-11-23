-- Chargebacks: an invoice whose payment the cardholder's bank reversed
-- is marked 'charged_back' (with the time we learned of it), so it no
-- longer counts as paid. The webhook does this when Mollie reports the
-- payment's full amount as charged back.
BEGIN;

ALTER TABLE public.invoices DROP CONSTRAINT IF EXISTS invoices_status_check;
ALTER TABLE public.invoices
    ADD CONSTRAINT invoices_status_check
        CHECK (status IN ('pending', 'paid', 'failed', 'refunded', 'charged_back'));

ALTER TABLE public.invoices
    ADD COLUMN IF NOT EXISTS charged_back_at TIMESTAMPTZ;

COMMIT;
