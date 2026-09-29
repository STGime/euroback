-- Scoped tokens can't be represented without the columns: remove them
-- (legacy account-wide tokens are kept).
DELETE FROM public.personal_access_tokens WHERE project_id IS NOT NULL;
DROP INDEX IF EXISTS public.ix_pat_project;
ALTER TABLE public.personal_access_tokens DROP CONSTRAINT IF EXISTS pat_scope_complete;
ALTER TABLE public.personal_access_tokens DROP COLUMN IF EXISTS role;
ALTER TABLE public.personal_access_tokens DROP COLUMN IF EXISTS project_id;
