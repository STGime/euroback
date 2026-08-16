-- 000102_pending_projects.up.sql
--
-- Payment-first project creation for paid plans (#406).
--
-- Today, clicking "Create Project" with Pro selected inserts the row
-- immediately with plan='pro' — no billing checkout runs, so users
-- get Pro features without paying. This table holds the "user
-- clicked Create + started Mollie checkout, waiting for payment"
-- state so no project row exists until Mollie confirms first
-- payment. On paid webhook, the pending row's contents feed
-- TenantService.CreateProject and the row is deleted.
--
-- Rows without a mollie_payment_id are user-clicked-then-abandoned
-- (Mollie was never called or the response was lost); the sweeper
-- expires those after 24h. Rows with a mollie_payment_id stay
-- until the webhook resolves them (created + deleted, or refunded
-- + deleted on provisioning failure).
--
-- Only 'pro' is on this path today; Team is closed-beta with a
-- separate beta-grant subscription flow, Free creates immediately
-- via POST /platform/projects.

BEGIN;

CREATE TABLE public.pending_projects (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id            UUID        NOT NULL REFERENCES public.platform_users(id) ON DELETE CASCADE,
    name                TEXT        NOT NULL,
    slug                TEXT        NOT NULL,
    region              TEXT        NOT NULL,
    plan                TEXT        NOT NULL CHECK (plan IN ('pro')),
    mollie_payment_id   TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at          TIMESTAMPTZ NOT NULL DEFAULT now() + interval '24 hours'
);

-- Owner lookup — sweeper + concurrent-click check both key on this.
CREATE INDEX idx_pending_projects_owner ON public.pending_projects(owner_id);

-- Sweeper query filter — hourly DELETE of expired rows without a
-- resolved payment. Partial index because expires_at is dense (every
-- row has one) but the WHERE clause is what the sweeper actually
-- matches.
CREATE INDEX idx_pending_projects_expires_unresolved
    ON public.pending_projects(expires_at)
    WHERE mollie_payment_id IS NULL;

-- Webhook lookup — Mollie's webhook posts back with a payment ID
-- which we lookup via metadata to find the pending row. Only rows
-- that have started checkout have this column set, so partial index.
CREATE UNIQUE INDEX idx_pending_projects_mollie_payment
    ON public.pending_projects(mollie_payment_id)
    WHERE mollie_payment_id IS NOT NULL;

-- Concurrent-click backstop (#407 review 🔴). NewProjectCheckout
-- takes an advisory lock keyed on owner_id to serialise the
-- guard-check + INSERT, but the advisory lock alone would still be
-- lost across a pod restart between the tx commits. This partial
-- unique index catches the escape hatch: at most one unresolved
-- pending row per owner can exist at any point. Two racing INSERTs
-- would have the second fail with 23505 — the service catches that
-- and returns ErrPendingCheckoutInFlight. Doesn't block a stale
-- unresolved row (with a resolved-but-expired mollie_payment_id)
-- from being INSERTed, but the sweeper deletes those hourly.
CREATE UNIQUE INDEX idx_pending_projects_owner_unresolved
    ON public.pending_projects(owner_id)
    WHERE mollie_payment_id IS NULL;

COMMENT ON TABLE public.pending_projects IS
    'Holds "user clicked Create on Pro + Mollie checkout in flight" state before a real project row exists. See migration 000102 and issue #406.';

COMMIT;
