-- The billing period an invoice covers, recorded when its payment is in
-- (first payment: payment date → first recurring charge; renewal: the
-- charge date → the next one). Before this the PDF took the period from
-- the subscription (start date → next charge at render time), which for
-- a renewal printed the whole subscription so far. NULL for older
-- invoices: the renderer then uses the payment date plus one month.
BEGIN;

ALTER TABLE public.invoices
    ADD COLUMN IF NOT EXISTS period_start TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS period_end   TIMESTAMPTZ;

COMMIT;
