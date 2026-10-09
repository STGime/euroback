-- 000140_platform_sql_log_keep_on_delete.up.sql
--
-- 1. Deleting a project no longer deletes its SQL log entries. The text,
--    error detail and IP are cleared — the text can contain the project's
--    data — but who sent what kind of statement, when, its hash and the
--    outcome stay until the normal 30-day cleanup. An entry written by a
--    request still in flight during the delete is scrubbed by the hourly
--    cleanup (developer pool, hence its column UPDATE grant). Projects are deleted by
--    DeleteProject and, with their owner, by account deletion (cascade);
--    a trigger covers both.
-- 2. New source 'cron': SQL saved in cron jobs and console test runs.
--
-- SECURITY DEFINER convention (AGENTS.md): no PUBLIC EXECUTE. The trigger
-- function runs as its owner (the migrator) so the deleting role needs no
-- UPDATE on the log; trigger invocation doesn't check EXECUTE.
-- No explicit BEGIN/COMMIT: golang-migrate wraps the file.

ALTER TABLE public.platform_sql_log
    DROP CONSTRAINT IF EXISTS platform_sql_log_project_id_fkey;

ALTER TABLE public.platform_sql_log
    ADD COLUMN IF NOT EXISTS project_deleted_at TIMESTAMPTZ;

ALTER TABLE public.platform_sql_log
    DROP CONSTRAINT IF EXISTS platform_sql_log_source_check;
ALTER TABLE public.platform_sql_log
    ADD CONSTRAINT platform_sql_log_source_check
    CHECK (source IN ('sql', 'sql_transaction', 'function', 'policy', 'migration', 'cron'));

CREATE OR REPLACE FUNCTION public.platform_sql_log_scrub_deleted_project()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
BEGIN
    UPDATE public.platform_sql_log
       SET statement = '', detail = NULL, ip = NULL, project_deleted_at = now()
     WHERE project_id = OLD.id
       AND project_deleted_at IS NULL;
    RETURN OLD;
END;
$$;

REVOKE EXECUTE ON FUNCTION public.platform_sql_log_scrub_deleted_project() FROM PUBLIC;

DROP TRIGGER IF EXISTS platform_sql_log_scrub_on_project_delete ON public.projects;
CREATE TRIGGER platform_sql_log_scrub_on_project_delete
    AFTER DELETE ON public.projects
    FOR EACH ROW EXECUTE FUNCTION public.platform_sql_log_scrub_deleted_project();

GRANT UPDATE (statement, detail, ip, project_deleted_at) ON public.platform_sql_log TO eurobase_developer;
