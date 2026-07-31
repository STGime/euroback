-- 000082_invoice_sequence.up.sql
--
-- PR 6.5 of the billing stack (docs/billing/stacked-pr-plan.md).
-- Two related compliance-hardening additions:
--
--   1. Replace the random UUID-slice invoice number used in PR 6
--      with a gap-free, monotonically increasing sequence.
--      Required by EU VAT Directive Art 220-231 for VAT invoices
--      and treated as best practice by Estonian tax auditors even
--      for non-VAT-registered entities like ours.
--
--   2. Add invoice_mail_sent_at TIMESTAMPTZ. The auto-mail path
--      (PR 6.5 invoice_email.go) uses an atomic
--      "claim-then-send" UPDATE keyed on this column so a race
--      between the async webhook render and the on-demand
--      download-endpoint render can't send two buyer mails +
--      two BCC copies.
--
-- Design:
--
--  * Global Postgres SEQUENCE, not per-year. The year in the
--    display format (EB-YYYY-NNNNNN) comes from
--    invoice.created_at at render time and is a rendering
--    concern, not a schema concern.
--
--  * BIGINT column, NOT NULL DEFAULT nextval(...), UNIQUE
--    constraint. INSERTs that omit the value get the next
--    number; the UNIQUE catches an operator manually writing a
--    duplicate via psql.
--
--  * LOCK TABLE ... IN SHARE ROW EXCLUSIVE MODE at the top of
--    the tx. Blocks concurrent INSERTs during the backfill →
--    NOT NULL flip window; otherwise a gateway INSERT landing
--    between the UPDATE and the SET NOT NULL leaves a NULL row
--    that hard-fails the constraint, blocking the deploy.
--
--  * Backfill: assign sequence values to any existing invoices
--    (PR 3/6 test rows). ORDER BY (created_at, id) so tied
--    timestamps sort deterministically — staging dry-run and
--    prod run yield identical numbering. Then setval the
--    sequence past the largest backfilled value.
--
--  * "Gap-free" nuance: rolled-back INSERTs consume sequence
--    values that never appear in invoice_number. That's fine —
--    Estonian and EU-VAT interpretation of "gap-free" is
--    "no missing numbers in the PERSISTED set", not "every
--    nextval() has a matching row". Rolled-back numbers simply
--    aren't part of our invoice sequence and don't need to be
--    accounted for.

BEGIN;

-- Block concurrent INSERTs during the backfill → NOT NULL flip.
LOCK TABLE public.invoices IN SHARE ROW EXCLUSIVE MODE;

CREATE SEQUENCE IF NOT EXISTS public.invoice_number_seq;

ALTER TABLE public.invoices
    ADD COLUMN IF NOT EXISTS invoice_number BIGINT,
    ADD COLUMN IF NOT EXISTS invoice_mail_sent_at TIMESTAMPTZ;

-- Backfill existing rows in (created_at, id) order — id
-- tiebreaker ensures deterministic numbering across environments
-- with tied timestamps.
UPDATE public.invoices AS i
   SET invoice_number = ranked.rn
  FROM (
    SELECT id, row_number() OVER (ORDER BY created_at ASC, id ASC) AS rn
      FROM public.invoices
     WHERE invoice_number IS NULL
  ) AS ranked
 WHERE i.id = ranked.id;

-- Advance the sequence past the largest backfilled value so
-- fresh inserts continue where the backfill left off.
-- Third arg `false` means "the passed value IS the next
-- nextval() will return" (as opposed to "the passed value was
-- the last returned").
SELECT setval(
    'public.invoice_number_seq',
    COALESCE((SELECT MAX(invoice_number) FROM public.invoices), 0) + 1,
    false
);

ALTER TABLE public.invoices
    ALTER COLUMN invoice_number SET DEFAULT nextval('public.invoice_number_seq'),
    ALTER COLUMN invoice_number SET NOT NULL,
    ADD CONSTRAINT invoices_invoice_number_key UNIQUE (invoice_number);

COMMIT;
