BEGIN;
DROP INDEX IF EXISTS public.idx_projects_idle_pause_candidates;
ALTER TABLE public.projects DROP CONSTRAINT IF EXISTS projects_state_check;
ALTER TABLE public.projects
    DROP COLUMN IF EXISTS grandfathered_until,
    DROP COLUMN IF EXISTS last_active_at,
    DROP COLUMN IF EXISTS state;
COMMIT;
