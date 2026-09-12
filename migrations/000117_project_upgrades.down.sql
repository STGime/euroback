-- 000117_project_upgrades.down.sql

DROP TRIGGER IF EXISTS trg_project_upgrades_touch_updated_at ON public.project_upgrades;
DROP FUNCTION IF EXISTS public.project_upgrades_touch_updated_at();
DROP TABLE IF EXISTS public.project_upgrades;

ALTER TABLE public.projects DROP COLUMN IF EXISTS maintenance_mode;
