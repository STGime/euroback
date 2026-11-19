-- 000155_edge_functions_allow_service_role.up.sql
--
-- Per-function setting for ctx.db.asService(): SQL an edge function runs
-- with the service identity while a signed-in user is calling it (the
-- user's id stays set, so auth_uid() and audit triggers still see who
-- asked). Off by default; a developer switches it on per function.
--
-- The runner reads the setting through public.runner_get_function and
-- refuses asService() SQL for a function that doesn't have it.
--
-- The lookup's result gains a column, which needs DROP + CREATE (same
-- transaction; golang-migrate wraps the file). Runner pods from before
-- this migration select the old columns by name and keep working.

ALTER TABLE public.edge_functions
    ADD COLUMN IF NOT EXISTS allow_service_role boolean NOT NULL DEFAULT false;

DROP FUNCTION public.runner_get_function(uuid, uuid);

CREATE FUNCTION public.runner_get_function(
    p_function_id uuid,
    p_project_id  uuid
)
RETURNS TABLE(
    code                 text,
    env_vars_legacy      jsonb,
    env_vars_blob        bytea,
    env_vars_nonce       bytea,
    env_vars_key_version smallint,
    schema_name          text,
    allow_service_role   boolean
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
           p.schema_name,
           ef.allow_service_role
      FROM public.edge_functions ef
      JOIN public.projects p ON p.id = ef.project_id
     WHERE ef.id = p_function_id
       AND ef.project_id = p_project_id
       AND ef.status = 'active'
$$;

ALTER FUNCTION public.runner_get_function(uuid, uuid) OWNER TO eurobase_migrator;
REVOKE ALL ON FUNCTION public.runner_get_function(uuid, uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.runner_get_function(uuid, uuid) TO eurobase_function_runner;
