-- 000100_drop_platform_tenant_schema_for_team_tier.up.sql
--
-- Remove stale platform-DB tenant schemas for Team-tier / Legal-Team
-- projects. Rationale: Team-tier projects live on a dedicated Scaleway
-- managed-PG instance; the CreateProject flow before this migration
-- ALSO created a duplicate tenant schema on the platform DB via
-- provision_tenant(). That duplicate would diverge from the dedicated
-- copy the moment console / SDK traffic routed there — silent data loss.
--
-- Safe to drop: confirmed no live Team-tier customers in prod
-- (myteam2 is the only affected project and is a test artifact).
-- Every DROP uses IF EXISTS so a project whose schema was already
-- absent (e.g. bootstrap-time failure) doesn't blow up the migration.
--
-- Paired with the CreateProject change that skips provision_tenant on
-- the platform DB for team/legal_team plans going forward.
--
-- ── Guard predicates (belt + suspenders) ─────────────────────────────
-- `plan` is MUTABLE — billing/webhook.go's subscription handler does
-- `UPDATE projects SET plan = $1` when a checkout completes. A Free/Pro
-- project that later gets upgraded to Team keeps its live data on the
-- platform DB (no dedicated instance was ever provisioned for it), and
-- dropping that schema would destroy the only copy. Two predicates
-- below make this migration idempotent even against that class of row:
--
--   1. schema_name LIKE 'tenant\_%' — refuse to touch anything whose
--      schema_name doesn't match the tenant_<id> convention. Defensive
--      against hand-edited values or a future schema_name that points
--      at something shared (public, api, …).
--
--   2. EXISTS (project_databases row) — a Team-tier row without a
--      dedicated instance has no other place its data could live.
--      Only drop when there's a provably present dedicated copy.

BEGIN;

DO $$
DECLARE
    v_row RECORD;
    v_dropped INT := 0;
BEGIN
    FOR v_row IN
        SELECT p.id, p.schema_name
          FROM public.projects p
         WHERE p.plan IN ('team', 'legal_team')
           AND p.schema_name IS NOT NULL
           AND p.schema_name LIKE 'tenant\_%' ESCAPE '\'
           AND EXISTS (
               SELECT 1
                 FROM public.project_databases pd
                WHERE pd.project_id = p.id
           )
    LOOP
        EXECUTE format('DROP SCHEMA IF EXISTS %I CASCADE', v_row.schema_name);
        v_dropped := v_dropped + 1;
        RAISE NOTICE 'dropped platform-DB tenant schema for team project % (%)',
            v_row.id, v_row.schema_name;
    END LOOP;
    RAISE NOTICE 'total platform-DB tenant schemas dropped: %', v_dropped;
END $$;

-- Nit acknowledged (PR-A review): DROP SCHEMA CASCADE leaves the
-- per-tenant NOLOGIN <schema>_func / <schema>_ddl roles behind (they
-- live in pg_roles, not the schema). Harmless — no LOGIN, no grants
-- to platform tables — and cleaning them here is out of scope
-- because 000047 (function_runner) grants membership on the func
-- role to eurobase_function_runner, and a DROP ROLE would need the
-- reciprocal REVOKE first. Left as future clutter cleanup, not a
-- correctness issue.

COMMIT;
