-- 000056_pending_projects.up.sql
--
-- Closes #70. Selecting "Pro" at project creation used to write a row
-- with plan='pro' without charging the user, then the project sat
-- there free-with-pro-label. The new flow: when plan='pro' is
-- requested at signup, we hold the project intent in pending_projects,
-- start a Mollie checkout, and provision the real public.projects row
-- only when the payment.paid webhook lands.
--
-- This table is the parking spot. Owned by the migrator role to match
-- the rest of public.* (gateway role gets DML grants further down).

CREATE TABLE IF NOT EXISTS public.pending_projects (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id            UUID NOT NULL REFERENCES public.platform_users(id) ON DELETE CASCADE,
    name                TEXT NOT NULL,
    slug                TEXT NOT NULL,
    region              TEXT NOT NULL DEFAULT 'fr-par',
    plan                TEXT NOT NULL,
    mollie_payment_id   TEXT NULL UNIQUE,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at          TIMESTAMPTZ NOT NULL DEFAULT (now() + interval '24 hours')
);

-- Lookup path on the webhook: given a Mollie payment ID, find the
-- pending row to materialise. Unique constraint on mollie_payment_id
-- prevents double-provisioning if the webhook arrives twice.
CREATE INDEX IF NOT EXISTS idx_pending_projects_payment
    ON public.pending_projects (mollie_payment_id);

-- Sweeper scan path. The expires_at default of 24h gives the user a
-- generous window to complete checkout; rows past that are orphaned
-- intent and get hard-deleted by the dunning worker.
CREATE INDEX IF NOT EXISTS idx_pending_projects_expires
    ON public.pending_projects (expires_at);

-- Slug uniqueness against materialised projects is enforced by the
-- existing public.projects.slug unique constraint at provision time;
-- here we only guarantee no two pending rows hold the same slug for
-- the same owner concurrently (collisions surface as a constraint
-- violation at INSERT time and the handler maps that to a 409).
CREATE UNIQUE INDEX IF NOT EXISTS idx_pending_projects_owner_slug
    ON public.pending_projects (owner_id, slug);

GRANT SELECT, INSERT, UPDATE, DELETE ON public.pending_projects TO eurobase_gateway;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.pending_projects TO eurobase_developer;
