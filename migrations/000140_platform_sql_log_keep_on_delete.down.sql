DROP TRIGGER IF EXISTS platform_sql_log_scrub_on_project_delete ON public.projects;
DROP FUNCTION IF EXISTS public.platform_sql_log_scrub_deleted_project();

-- Entries of deleted projects can't satisfy the foreign key again.
DELETE FROM public.platform_sql_log l
 WHERE NOT EXISTS (SELECT 1 FROM public.projects p WHERE p.id = l.project_id);
DELETE FROM public.platform_sql_log WHERE source = 'cron';

ALTER TABLE public.platform_sql_log DROP CONSTRAINT IF EXISTS platform_sql_log_source_check;
ALTER TABLE public.platform_sql_log
    ADD CONSTRAINT platform_sql_log_source_check
    CHECK (source IN ('sql', 'sql_transaction', 'function', 'policy', 'migration'));

ALTER TABLE public.platform_sql_log DROP COLUMN IF EXISTS project_deleted_at;

ALTER TABLE public.platform_sql_log
    ADD CONSTRAINT platform_sql_log_project_id_fkey
    FOREIGN KEY (project_id) REFERENCES public.projects(id) ON DELETE CASCADE;
