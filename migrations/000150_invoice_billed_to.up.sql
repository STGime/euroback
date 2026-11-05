-- Freeze the billing party on each invoice (#846, ownership-transfer review).
--
-- Invoice access and the legal name / address / VAT printed on the PDF were
-- resolved through projects.owner_id at read time. That's wrong once a
-- project can change owner: after a transfer the new owner would see (and
-- re-render) the previous owner's historical invoices, leaking their billing
-- identity and corrupting issued records. billed_to_user_id snapshots who
-- was billed when the invoice was issued; reads filter and render by it.
ALTER TABLE public.invoices
    ADD COLUMN IF NOT EXISTS billed_to_user_id UUID REFERENCES public.platform_users(id) ON DELETE SET NULL;

-- Backfill existing invoices with the project's current owner (the party
-- that has been billed to date — no transfers have happened yet).
UPDATE public.invoices i
   SET billed_to_user_id = p.owner_id
  FROM public.projects p
 WHERE p.id = i.project_id
   AND i.billed_to_user_id IS NULL;

CREATE INDEX IF NOT EXISTS idx_invoices_billed_to ON public.invoices(billed_to_user_id);
