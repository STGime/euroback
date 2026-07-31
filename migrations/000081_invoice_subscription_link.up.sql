-- 000081_invoice_subscription_link.up.sql
--
-- Address PR 6 review (HIGH): invoices have no FK to the
-- subscription they belong to. Today loadInvoiceData picks the
-- newest subscription by started_at DESC — after any
-- cancel+resubscribe, re-rendering an old invoice silently
-- rewrites its period range and plan name to the new
-- subscription's. This also breaks the "deterministic re-render"
-- invariant that makes the async-vs-on-demand race safe.
--
-- Fix: add invoices.subscription_id UUID FK, populate at INSERT
-- in PR 3's checkout flow and PR 4's recurring-payment flow,
-- and re-shape loadInvoiceData to JOIN via subscription_id
-- instead of "most recent for project".
--
-- Nullable because:
--   1. Existing (test) invoices from PR 3/4 pre-date this column
--      and can't be retroactively linked.
--   2. First-payment invoices are inserted BEFORE the recurring
--      Mollie subscription exists — for those the field will be
--      populated by the webhook's activate transition (PR 4) so
--      the value gets stamped once we know it.
--
-- No CASCADE — subscriptions.status='canceled' rows stay for
-- audit; nulling invoice.subscription_id on subscription delete
-- would defeat the whole point of this migration.

BEGIN;

ALTER TABLE public.invoices
    ADD COLUMN IF NOT EXISTS subscription_id UUID
        REFERENCES public.subscriptions(id) ON DELETE SET NULL;

-- Best-effort backfill for any historical invoices: link to
-- the earliest live subscription for the project. This produces
-- the right answer when the project has never cycled through
-- cancel+resubscribe (the common case). Once a project cycles,
-- old invoices with NULL subscription_id will fall back to the
-- LEFT JOIN pattern loadInvoiceData already tolerates.
UPDATE public.invoices i
   SET subscription_id = s.id
  FROM (
        SELECT DISTINCT ON (project_id) id, project_id
          FROM public.subscriptions
         WHERE status IN ('active', 'past_due', 'canceled', 'expired', 'incomplete')
         ORDER BY project_id, started_at ASC NULLS LAST
  ) s
 WHERE s.project_id = i.project_id
   AND i.subscription_id IS NULL;

-- Index for the reverse lookup PR 6's loadInvoiceData does
-- (invoice -> subscription); partial to avoid indexing the
-- pre-migration NULLs that never get populated.
CREATE INDEX IF NOT EXISTS idx_invoices_subscription_id
    ON public.invoices (subscription_id)
    WHERE subscription_id IS NOT NULL;

COMMIT;
