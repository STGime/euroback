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

BEGIN;

DO $$
DECLARE
    v_row RECORD;
    v_dropped INT := 0;
BEGIN
    FOR v_row IN
        SELECT id, schema_name
          FROM public.projects
         WHERE plan IN ('team', 'legal_team')
           AND schema_name IS NOT NULL
    LOOP
        EXECUTE format('DROP SCHEMA IF EXISTS %I CASCADE', v_row.schema_name);
        v_dropped := v_dropped + 1;
        RAISE NOTICE 'dropped platform-DB tenant schema for team project % (%)',
            v_row.id, v_row.schema_name;
    END LOOP;
    RAISE NOTICE 'total platform-DB tenant schemas dropped: %', v_dropped;
END $$;

COMMIT;
