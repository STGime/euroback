BEGIN;

ALTER TABLE public.invoices
    DROP COLUMN IF EXISTS period_end,
    DROP COLUMN IF EXISTS period_start;

COMMIT;
