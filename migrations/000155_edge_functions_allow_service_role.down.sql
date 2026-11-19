-- 000155_edge_functions_allow_service_role.down.sql

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
REVOKE ALL ON FUNCTION public.runner_get_function(uuid, uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.runner_get_function(uuid, uuid) TO eurobase_function_runner;

ALTER TABLE public.edge_functions DROP COLUMN IF EXISTS allow_service_role;
