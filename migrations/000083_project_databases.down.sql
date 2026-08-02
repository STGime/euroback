-- 000083_project_databases.down.sql
--
-- Reverses 000083_project_databases.up.sql.

BEGIN;

DROP TRIGGER IF EXISTS trg_project_databases_touch_updated_at ON public.project_databases;
DROP FUNCTION IF EXISTS public.project_databases_touch_updated_at();
DROP TABLE IF EXISTS public.project_databases;

COMMIT;
