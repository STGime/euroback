-- 000092_live_index_excludes_restoring.down.sql

BEGIN;

DROP INDEX IF EXISTS public.ux_project_databases_live_one_per_project;

CREATE UNIQUE INDEX ux_project_databases_live_one_per_project
    ON public.project_databases(project_id)
    WHERE state IN ('provisioning', 'active', 'restoring')
      AND deleted_at IS NULL;

COMMIT;
