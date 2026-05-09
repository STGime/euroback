-- 000049_vault_get_for_runner.up.sql
--
-- Closes #79: ctx.vault.get(name) inside an edge function always returned
-- null because the runner had no way to read tenant_*.vault_secrets — RLS
-- on that table requires public.is_service_role(), and the runner connects
-- as eurobase_function_runner which is not a service role.
--
-- This migration adds a single SECURITY DEFINER function that the runner
-- can call to fetch the encrypted blob + nonce for a given (project_id,
-- secret_name) tuple. The runner decrypts locally using
-- VAULT_ENCRYPTION_KEY (already present in its env via the
-- eurobase-secrets k8s Secret) — the encryption key never leaves the
-- pod, the SECURITY DEFINER scope is keyed on (project_id, name) so
-- there is no enumeration path, and EXECUTE is granted only to
-- eurobase_function_runner.

BEGIN;

CREATE OR REPLACE FUNCTION public.vault_get_for_runner(
    p_project_id uuid,
    p_name       text
)
RETURNS TABLE(encrypted bytea, nonce bytea)
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
    -- safe to interpolate via %I.
    RETURN QUERY EXECUTE format(
        'SELECT secret, nonce FROM %I.vault_secrets WHERE name = $1',
        v_schema
    ) USING p_name;
END;
$$;

ALTER FUNCTION public.vault_get_for_runner(uuid, text) OWNER TO eurobase_migrator;
REVOKE ALL ON FUNCTION public.vault_get_for_runner(uuid, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.vault_get_for_runner(uuid, text) TO eurobase_function_runner;

COMMIT;
