-- 000079_billing_schema.down.sql
--
-- Reverses 000079_billing_schema.up.sql. Restores the pre-billing
-- schema shape so a rollback boots cleanly against the previous
-- code revision (which reads only the original columns).
--
-- Order matters: drop the new UNIQUE + partial indexes before the
-- columns they reference; restore the old CHECK before the new one
-- is dropped so the table is always constrained.

BEGIN;

-- ── invoices ───────────────────────────────────────────────────────

ALTER TABLE public.invoices
    DROP COLUMN IF EXISTS pdf_object_key;

-- ── subscriptions ──────────────────────────────────────────────────

DROP INDEX IF EXISTS idx_subscriptions_project_live;
DROP INDEX IF EXISTS idx_subscriptions_past_due_since;
DROP INDEX IF EXISTS idx_subscriptions_next_charge_at;

ALTER TABLE public.subscriptions
    DROP CONSTRAINT IF EXISTS subscriptions_status_check;

-- Any rows in the new-only states must be rolled to a legacy state
-- before the old CHECK can be re-applied. 'incomplete' had no
-- pre-billing analogue; the only safe rollback destination is
-- 'cancelled' (British spelling, per the pre-billing enum).
UPDATE public.subscriptions
   SET status = 'cancelled'
 WHERE status IN ('incomplete', 'past_due', 'canceled', 'expired');

-- Restore the original enum.
ALTER TABLE public.subscriptions
    ADD CONSTRAINT subscriptions_status_check
        CHECK (status IN ('active', 'cancelled', 'overdue'));

ALTER TABLE public.subscriptions
    ALTER COLUMN status SET DEFAULT 'active';

ALTER TABLE public.subscriptions
    DROP COLUMN IF EXISTS past_due_since,
    DROP COLUMN IF EXISTS canceled_at,
    DROP COLUMN IF EXISTS next_charge_at,
    DROP COLUMN IF EXISTS started_at,
    DROP COLUMN IF EXISTS billing_interval,
    DROP COLUMN IF EXISTS currency,
    DROP COLUMN IF EXISTS price_cents,
    DROP COLUMN IF EXISTS mollie_customer_id;

COMMIT;
