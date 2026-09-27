-- 000131_runner_get_tenant_db.up.sql
--
-- #676: where a project's tenant data lives, for the edge-functions
-- runner. A Team / Legal-Team project keeps it on a dedicated instance;
-- the runner must run ctx.db.sql and ctx.vault there — never on the
-- shared cluster, which has no schema for it (or a stale copy after a
-- Pro→Team upgrade).
--
-- Returns one row, or none:
--   * none → the project uses the shared cluster;
--   * state 'upgrading' / 'restoring' / 'maintenance' (no host) while an
--     upgrade is in flight (any state: the new row turns active before the
--     data copy), a restore is cutting over (before that the old instance
--     keeps serving, as for the SDK), or the project is in maintenance
--     with a dedicated database: the data is moving, and the runner (whose
--     cron invocations don't pass the gateway's maintenance middleware)
--     must not write to either side. An in-flight upgrade counts even
--     before its project_databases row exists (a Pro project's writes
--     during the copy would be lost);
--   * otherwise the live project_databases row (same rule as the gateway's
--     GetLiveByProject: provisioning / active / restoring, not deleted,
--     active first). The runner refuses anything but 'active'
--     (retryable) instead of falling back.
--
-- Host / port / database / instance id only — no credentials: the runner
-- logs in as <schema>_func with the password it derives from
-- FUNC_PASSWORD_SECRET and the instance id (the worker's
-- tenantlogin.EnsureTeam applies it on the instance).
--
-- SECURITY DEFINER convention (AGENTS.md): no PUBLIC EXECUTE; the runner
-- role only. No explicit BEGIN/COMMIT: golang-migrate wraps the file.

CREATE OR REPLACE FUNCTION public.runner_get_tenant_db(p_project_id uuid)
RETURNS TABLE(id uuid, host text, port integer, database_name text, state text)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM public.project_upgrades u
                WHERE u.project_id = p_project_id
                  AND u.state IN ('requested', 'provisioning', 'copying', 'cutting_over')) THEN
        RETURN QUERY SELECT NULL::uuid, NULL::text, NULL::integer, NULL::text, 'upgrading'::text;
        RETURN;
    END IF;
    IF EXISTS (SELECT 1 FROM public.restore_operations r
                WHERE r.project_id = p_project_id
                  AND r.state = 'cutover') THEN
        RETURN QUERY SELECT NULL::uuid, NULL::text, NULL::integer, NULL::text, 'restoring'::text;
        RETURN;
    END IF;
    IF EXISTS (SELECT 1 FROM public.projects p
                 JOIN public.project_databases pd ON pd.project_id = p.id
                WHERE p.id = p_project_id AND p.maintenance_mode
                  AND pd.state IN ('provisioning', 'active', 'restoring')
                  AND pd.deleted_at IS NULL) THEN
        RETURN QUERY SELECT NULL::uuid, NULL::text, NULL::integer, NULL::text, 'maintenance'::text;
        RETURN;
    END IF;
    RETURN QUERY
    SELECT pd.id, pd.host, pd.port, pd.database_name, pd.state
      FROM public.project_databases pd
     WHERE pd.project_id = p_project_id
       AND pd.state IN ('provisioning', 'active', 'restoring')
       AND pd.deleted_at IS NULL
     ORDER BY (pd.state = 'active') DESC, pd.created_at DESC
     LIMIT 1;
END
$$;

ALTER FUNCTION public.runner_get_tenant_db(uuid) OWNER TO eurobase_migrator;
REVOKE ALL ON FUNCTION public.runner_get_tenant_db(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.runner_get_tenant_db(uuid) TO eurobase_function_runner;
