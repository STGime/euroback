-- 000125_runner_get_function.up.sql
--
-- Narrow, SECURITY DEFINER lookup for the edge-functions runner, so the
-- runner role no longer needs direct table access to load function code.
-- Same pattern as vault_get_for_runner (000049).
--
-- Returns the function only when it belongs to the given project (the
-- runner passes the HMAC-verified X-Project-ID), is active, and exists.
-- Unknown / mismatched ids return no row — never raises.
--
-- Step 1 of 2: the runner switches to this function in the same release
-- (with a fallback while the function doesn't exist yet). A follow-up
-- migration removes the runner role's remaining direct grants on
-- public.* once this release is live — split so the old runner pods,
-- which still read the tables directly during rollout, keep working.
--
-- No explicit BEGIN/COMMIT: golang-migrate wraps each .up.sql in its own tx.

CREATE OR REPLACE FUNCTION public.runner_get_function(
    p_function_id uuid,
    p_project_id  uuid
)
RETURNS TABLE(
    code                 text,
    env_vars_legacy      jsonb,
    env_vars_blob        bytea,
    env_vars_nonce       bytea,
    env_vars_key_version smallint,
    schema_name          text
)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    SELECT COALESCE(ef.compiled_code, ef.code),
           ef.env_vars,
           ef.env_vars_blob,
           ef.env_vars_nonce,
           ef.env_vars_key_version,
           p.schema_name
      FROM public.edge_functions ef
      JOIN public.projects p ON p.id = ef.project_id
     WHERE ef.id = p_function_id
       AND ef.project_id = p_project_id
       AND ef.status = 'active'
$$;

ALTER FUNCTION public.runner_get_function(uuid, uuid) OWNER TO eurobase_migrator;
-- Per the SECURITY DEFINER convention (CLAUDE.md): no PUBLIC EXECUTE.
REVOKE ALL ON FUNCTION public.runner_get_function(uuid, uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.runner_get_function(uuid, uuid) TO eurobase_function_runner;
