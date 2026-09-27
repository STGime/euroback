-- 000131_runner_get_tenant_db.up.sql
--
-- #676: where a project's tenant data lives, for the edge-functions
-- runner. A Team / Legal-Team project keeps it on a dedicated instance;
-- the runner must run ctx.db.sql and ctx.vault there — never on the
-- shared cluster, which has no schema for it (or a stale copy after a
-- Pro→Team upgrade).
--
-- Returns the project's live project_databases row (same rule as the
-- gateway's PlatformTenantContext / GetLiveByProject: states
-- provisioning / active / restoring, not deleted; newest active first).
-- No row → the project uses the shared cluster. The runner refuses when
-- state isn't 'active' (retryable) instead of falling back.
--
-- Host / port / database only — no credentials: the runner logs in as
-- <schema>_func with the password it derives from FUNC_PASSWORD_SECRET
-- (the worker's tenantlogin.EnsureTeam applies it on the instance).
--
-- SECURITY DEFINER convention (AGENTS.md): no PUBLIC EXECUTE; the runner
-- role only. No explicit BEGIN/COMMIT: golang-migrate wraps the file.

CREATE OR REPLACE FUNCTION public.runner_get_tenant_db(p_project_id uuid)
RETURNS TABLE(host text, port integer, database_name text, state text)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    SELECT pd.host, pd.port, pd.database_name, pd.state
      FROM public.project_databases pd
     WHERE pd.project_id = p_project_id
       AND pd.state IN ('provisioning', 'active', 'restoring')
       AND pd.deleted_at IS NULL
     ORDER BY (pd.state = 'active') DESC, pd.created_at DESC
     LIMIT 1
$$;

ALTER FUNCTION public.runner_get_tenant_db(uuid) OWNER TO eurobase_migrator;
REVOKE ALL ON FUNCTION public.runner_get_tenant_db(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.runner_get_tenant_db(uuid) TO eurobase_function_runner;
