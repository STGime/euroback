-- 000082_invoice_sequence.up.sql
--
-- PR 6.5 of the billing stack (docs/billing/stacked-pr-plan.md).
-- Replaces the random UUID-slice invoice number used in PR 6
-- with a gap-free, monotonically increasing sequence — required
-- by Estonian Accounting Act §7 and by every EU tax auditor's
-- first-question rulebook.
--
-- Design:
--
--  * Global Postgres SEQUENCE, not a per-year one. Estonian law
--    doesn't require yearly reset; the year in the display
--    format (EB-YYYY-NNNNNN) comes from invoice.created_at and
--    is a rendering concern, not a schema concern.
--
--  * BIGINT column with NOT NULL DEFAULT nextval(...). Any
--    INSERT that doesn't provide the value gets the next number
--    — belt-and-suspenders with the app-level code that also
--    calls nextval() so we don't accidentally re-issue if a
--    caller misses the DEFAULT.
--
--  * UNIQUE constraint guarantees no duplicates even if two
--    transactions race on the sequence (Postgres sequences are
--    already gap-free across concurrent nextval calls, but the
--    UNIQUE catches an operator manually writing a duplicate
--    via psql).
--
--  * Backfill: assign sequence values to any existing invoices
--    (PR 3/6 test rows). Order by created_at so the numbering
--    matches issue order. Then set the sequence's start point
--    to max+1 so new invoices continue monotonically.
--
--  * No gaps: even a rolled-back INSERT consumes a sequence
--    value. That's fine — gap-free means "no missing numbers in
--    the persisted set", not "every number issued is present".
--    Rolled-back sequence values simply never appear in
--    invoices.invoice_number, which is legal.

BEGIN;

CREATE SEQUENCE IF NOT EXISTS public.invoice_number_seq;

ALTER TABLE public.invoices
    ADD COLUMN IF NOT EXISTS invoice_number BIGINT;

-- Backfill existing rows in created_at order, then flip to
-- NOT NULL + DEFAULT for new rows.
UPDATE public.invoices AS i
   SET invoice_number = ranked.rn
  FROM (
    SELECT id, row_number() OVER (ORDER BY created_at ASC) AS rn
      FROM public.invoices
     WHERE invoice_number IS NULL
  ) AS ranked
 WHERE i.id = ranked.id;

-- Advance the sequence past the largest backfilled value so
-- fresh inserts continue where the backfill left off.
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
