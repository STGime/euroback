DROP INDEX IF EXISTS public.idx_invoices_billed_to;
ALTER TABLE public.invoices DROP COLUMN IF EXISTS billed_to_user_id;
