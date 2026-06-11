-- Issue #201: vault_get_for_runner must return key_version.
--
-- Migration 000057 moved the gateway to per-tenant HKDF-derived keys —
-- every secret written since is sealed at key_version >= 1 — but the
-- runner's read helper (000049) only returned (encrypted, nonce), so the
-- runner kept decrypting with the raw master key (the version-0 key) and
-- ctx.vault.get returned null for every post-000057 secret.
--
-- Adding a return column requires DROP + CREATE (CREATE OR REPLACE cannot
-- change an OUT row type), so the owner/grants from 000049 are re-applied.
--
-- Rollout compatibility: a not-yet-updated runner selects explicit columns
-- (encrypted, nonce) and is unaffected by the extra column. The updated
-- runner selects key_version, so this migration must apply before the new
-- runner image rolls out — guaranteed by the CI pipeline (migrate Job
-- gates the Deployment roll).

BEGIN;

DROP FUNCTION IF EXISTS public.vault_get_for_runner(uuid, text);

CREATE FUNCTION public.vault_get_for_runner(
    p_project_id uuid,
    p_name       text
)
RETURNS TABLE(encrypted bytea, nonce bytea, key_version smallint)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
DECLARE
    v_schema text;
BEGIN
    -- Resolve the project's tenant schema. SECURITY DEFINER means we
    -- read public.projects with the function owner's privileges
    -- (eurobase_migrator), bypassing the runner role's lack of direct
    -- access. Empty result for unknown project_id — never raises so the
    -- runner just sees `not found` and replies null to the worker.
    SELECT schema_name INTO v_schema
    FROM public.projects
    WHERE id = p_project_id;

    IF v_schema IS NULL THEN
        RETURN;
    END IF;

    -- Format-then-USING the parameter avoids any SQL injection through
    -- p_name since it's bound as a parameter, not interpolated. The
    -- schema name comes from public.projects (UUID-derived) so it's
    -- safe to interpolate via %I. COALESCE covers any pre-000057 row
    -- where key_version is somehow NULL — those were sealed with the
    -- legacy (version 0) key.
    RETURN QUERY EXECUTE format(
        'SELECT secret, nonce, COALESCE(key_version, 0::smallint) FROM %I.vault_secrets WHERE name = $1',
        v_schema
    ) USING p_name;
END;
$$;

ALTER FUNCTION public.vault_get_for_runner(uuid, text) OWNER TO eurobase_migrator;
REVOKE ALL ON FUNCTION public.vault_get_for_runner(uuid, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.vault_get_for_runner(uuid, text) TO eurobase_function_runner;

COMMIT;
