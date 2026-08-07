-- 000095_storage_retention_policies.up.sql
--
-- Legal-Team M2b, follow-on to retention_holds (000089): per-prefix
-- WORM retention for storage objects.
--
-- The row-level erasure protection in retention_holds covers
-- database rows and (via target_type='object') one-off object holds
-- that ops has explicitly registered. That's not enough for the
-- GoBD §146 Abs. 4 AO "Unveränderbarkeit" obligation, which applies
-- to *every* invoice / tax record / mandant file the instant it
-- lands — the tenant can't remember to place a hold per upload.
--
-- Policy shape: a Legal-Team tenant declares "objects at prefix P
-- retain for N years under basis B". The storage upload path looks
-- up the longest-matching prefix and hands the retention window to
-- S3 as x-amz-object-lock-retain-until-date, so Scaleway Object
-- Storage enforces WORM at rest. A delete attempt against a locked
-- object returns AccessDenied → we translate to HTTP 409
-- object_locked with the retention_until.
--
-- Defaults ops usually seed for a fresh Legal-Team bucket:
--   * /invoices/*   10y  §257 HGB / §147 AO
--   * /tax/*        10y  §147 AO
--   * /mandant/*    6y   §50 BRAO
--
-- Not seeded here — the console UI (follow-up PR) lets the customer
-- add / edit / remove per-prefix policies during setup.

BEGIN;

CREATE TABLE public.storage_retention_policies (
    id               UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id       UUID        NOT NULL REFERENCES public.projects(id) ON DELETE CASCADE,

    -- Prefix match against the object key. Empty string matches
    -- everything in the bucket; longest-prefix-wins at resolution
    -- time. Trailing '/' is conventional but not required — we
    -- match by plain HasPrefix.
    prefix           TEXT        NOT NULL,

    -- S3 Object Lock mode. compliance = not even root creds can
    -- shorten retention; governance = ops with bypass permission
    -- can. Legal-tech customers want compliance mode; governance is
    -- exposed for other future retention use-cases (e.g. an audit
    -- window a customer can waive after review).
    mode             TEXT        NOT NULL CHECK (mode IN ('compliance','governance')),

    -- Retention window in years, evaluated at upload time as
    -- retain_until = upload_time + retention_years years.
    -- Positive integer only — a zero-year policy is meaningless
    -- and a negative one would set retain-until in the past.
    retention_years  INTEGER     NOT NULL CHECK (retention_years > 0 AND retention_years <= 100),

    -- Free-text so tenants can cite exact German paragraphs. Same
    -- convention as retention_holds.legal_basis.
    legal_basis      TEXT        NOT NULL CHECK (length(legal_basis) > 0),

    created_by       UUID        REFERENCES public.platform_users(id) ON DELETE SET NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- One policy per (project, prefix). Editing a prefix is UPDATE,
    -- not DELETE+INSERT — preserves audit continuity.
    UNIQUE (project_id, prefix)
);

-- Resolution lookup: upload path fetches every policy for a project
-- and picks the longest prefix that matches the object key. Small
-- table (dozens of rows per tenant), single-column index is enough.
CREATE INDEX ix_storage_retention_policies_project
    ON public.storage_retention_policies(project_id);

GRANT SELECT, INSERT, UPDATE, DELETE ON public.storage_retention_policies TO eurobase_gateway;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.storage_retention_policies TO eurobase_developer;

COMMIT;
