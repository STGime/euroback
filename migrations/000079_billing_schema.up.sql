-- 000079_billing_schema.up.sql
--
-- PR 2 of the billing stack (docs/billing/stacked-pr-plan.md).
-- Extends the existing subscriptions and invoices tables (created in
-- 000001) with the fields the Mollie billing flow needs: price,
-- billing interval, next-charge timestamp, past-due tracking, PDF
-- object storage key, and an expanded status enum.
--
-- Design notes:
--
--  * NO new table. The plan originally proposed a billing_customers
--    join table, but platform_users.mollie_customer_id already
--    exists (000001) and is 1:1 with the platform user. Keeping the
--    Mollie customer ID on platform_users saves a join on every
--    checkout and matches the shape Mollie itself exposes.
--
--  * NO RLS. subscriptions and invoices are platform-tier tables,
--    accessed only via the eurobase_developer pool (member of the
--    migrator role) — not from the SDK gateway pool (eurobase_gateway)
--    which has no GRANT on them at all. Isolation is by absence of
--    grant, not by policy filter.
--
--  * Status enum expands from ('active', 'cancelled', 'overdue') to
--    the canonical Mollie-derived set ('incomplete', 'active',
--    'past_due', 'canceled', 'expired'). American spelling of
--    'canceled' is deliberate — it matches Mollie's API surface.
--    'cancelled' (British) is retired; there are no existing rows in
--    prod so no data migration is needed. The down migration
--    restores the old enum for rollback safety.
--
--  * price_cents lives on subscriptions rather than plans because
--    although grandfathering was dropped for the launch, per-sub
--    pricing is still needed for coupon codes and promotional pricing
--    (not scoped here — the column is future-proofing).
--
--  * next_charge_at mirrors Mollie's nextPaymentDate. The downgrade
--    cron (PR 5) reads this to schedule prorated-refund windows;
--    keeping it as a stored column avoids an outbound Mollie API
--    call per project on every cron tick.

BEGIN;

-- ── subscriptions ──────────────────────────────────────────────────

-- Drop the old status CHECK so we can widen it. Constraint name is
-- Postgres-generated ("subscriptions_status_check"); look it up
-- dynamically for portability across dev boxes with different
-- constraint-naming timing.
ALTER TABLE public.subscriptions
    DROP CONSTRAINT IF EXISTS subscriptions_status_check;

ALTER TABLE public.subscriptions
    ADD COLUMN IF NOT EXISTS mollie_customer_id  TEXT,
    ADD COLUMN IF NOT EXISTS price_cents         INTEGER,
    ADD COLUMN IF NOT EXISTS currency            CHAR(3)     NOT NULL DEFAULT 'EUR',
    ADD COLUMN IF NOT EXISTS billing_interval    TEXT        NOT NULL DEFAULT '1 month',
    ADD COLUMN IF NOT EXISTS started_at          TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS next_charge_at      TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS canceled_at         TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS past_due_since      TIMESTAMPTZ;

-- Re-add the CHECK with the canonical Mollie-aligned enum.
-- 'incomplete' is set by CreateCheckout before Mollie confirms first
-- payment; 'active' is set by the webhook when first_payment=paid;
-- 'past_due' when a recurring charge fails; 'canceled' on customer
-- cancel; 'expired' when Mollie's own subscription resource ends.
ALTER TABLE public.subscriptions
    ADD CONSTRAINT subscriptions_status_check
        CHECK (status IN ('incomplete', 'active', 'past_due', 'canceled', 'expired'));

-- Change default status: new rows start in 'incomplete' (waiting for
-- first payment) rather than 'active' (which was correct only in a
-- world where the platform_users.plan flip pre-dated any Mollie
-- interaction).
ALTER TABLE public.subscriptions
    ALTER COLUMN status SET DEFAULT 'incomplete';

-- Cron worker (PR 5) scans by these two columns hourly; without
-- indexes the scan is a full-table read once we have real volume.
CREATE INDEX IF NOT EXISTS idx_subscriptions_next_charge_at
    ON public.subscriptions (next_charge_at)
    WHERE next_charge_at IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_subscriptions_past_due_since
    ON public.subscriptions (past_due_since)
    WHERE past_due_since IS NOT NULL;

-- Partial unique index: at most one non-terminal subscription per
-- project. Prevents a race where two concurrent CreateCheckout
-- transactions each create an 'incomplete' row before either commits.
-- The set ('incomplete', 'active', 'past_due') captures every state
-- where the project should be treated as "has a live subscription".
CREATE UNIQUE INDEX IF NOT EXISTS idx_subscriptions_project_live
    ON public.subscriptions (project_id)
    WHERE status IN ('incomplete', 'active', 'past_due');

-- ── invoices ───────────────────────────────────────────────────────

-- pdf_object_key is the Scaleway Object Storage key where the
-- rendered invoice PDF lives. Set by the invoice-render worker
-- (PR 6); nullable because a paid invoice may exist for a few
-- seconds before the async render completes.
ALTER TABLE public.invoices
    ADD COLUMN IF NOT EXISTS pdf_object_key TEXT;

-- Index for the webhook idempotency guard: on every Mollie payment
-- webhook we upsert on mollie_payment_id (already UNIQUE from
-- 000001, so no extra index needed). This comment stays to prevent
-- future migrations from adding a redundant index.

COMMIT;
