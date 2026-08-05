-- 000089_retention_holds.up.sql
--
-- Team-tier M2b, hard blocker #3 for the German legal-tech beta:
-- reconcile Art 17 GDPR right-to-erasure with German legal-retention
-- obligations.
--
-- Applicable statutes a legal-tech tenant is subject to:
--   * §50 BRAO   — 6y lawyer file (Handakte) retention
--   * §257 HGB  — 10y commercial records
--   * §147 AO   — 10y tax records
--
-- When an end-user requests erasure (DSAR Article 17), the tenant
-- must refuse deletion of items still inside these retention windows
-- and report them back to the user as "retained under <basis>,
-- purgeable after <date>" instead of silently deleting or silently
-- keeping.
--
-- Table shape:
--
--   * target_type + target_ref polymorphism — one hold can cover a
--     database row (target_ref JSONB with pkey), a storage object
--     (target_ref JSONB with bucket + key), or a table-wide policy
--     (target_ref JSONB with schema + table). The internal/
--     compliance/retention_holds.go helper decodes based on
--     target_type.
--
--   * expires_at is authoritative — a daily worker sweeps
--     WHERE expires_at < now() and hard-deletes the row so the
--     underlying data becomes erasable again on the next DSAR.
--
--   * Legal basis is free-text so tenants can cite specific statutes
--     (§50 BRAO, §147 AO, custom obligation) without us maintaining
--     a controlled vocabulary.
--
-- Gated to the Legal-Team tier: only projects with
-- plan_limits.dedicated_db + legal_team plan can call the endpoints
-- that INSERT here. Enforcement via CheckLegalTeamTier at handler
-- time.

BEGIN;

CREATE TABLE public.retention_holds (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id   UUID        NOT NULL REFERENCES public.projects(id) ON DELETE CASCADE,

    -- Polymorphic target discriminator. Constraint keeps the set
    -- small — new target types require a code change AND a
    -- migration, not just a caller passing a new string.
    target_type  TEXT        NOT NULL CHECK (target_type IN ('row','object','table')),
    target_ref   JSONB       NOT NULL,

    -- Free-text so tenants can cite exact German paragraphs. Kept
    -- non-empty via CHECK to catch UI mistakes.
    legal_basis  TEXT        NOT NULL CHECK (length(legal_basis) > 0),

    expires_at   TIMESTAMPTZ NOT NULL,

    -- Nullable — a hold can be programmatically inserted by a cron
    -- (e.g. auto-hold every invoice at creation time for 10 years).
    created_by   UUID        REFERENCES public.platform_users(id) ON DELETE SET NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Erasure lookups filter by (project_id, target_type, target_ref) —
-- the DSAR path checks "is this row under hold?" once per candidate.
-- JSONB has a GIN index; combined with project_id + target_type it
-- keeps the check to microseconds.
CREATE INDEX ix_retention_holds_lookup
    ON public.retention_holds(project_id, target_type)
    WHERE expires_at > now();

CREATE INDEX ix_retention_holds_targetref
    ON public.retention_holds USING GIN (target_ref jsonb_path_ops);

-- Sweeper query: expired holds. Small index; the sweeper runs
-- daily and expects O(dozens) rows per run.
CREATE INDEX ix_retention_holds_expiry
    ON public.retention_holds(expires_at)
    WHERE expires_at > '1970-01-01';

GRANT SELECT, INSERT, DELETE ON public.retention_holds TO eurobase_gateway;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.retention_holds TO eurobase_developer;

COMMIT;
