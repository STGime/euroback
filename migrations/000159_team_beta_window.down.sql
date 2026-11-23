DROP INDEX IF EXISTS public.ix_projects_beta_ends_at;
ALTER TABLE public.projects
    DROP COLUMN IF EXISTS beta_expired_at,
    DROP COLUMN IF EXISTS beta_reminded_at,
    DROP COLUMN IF EXISTS beta_ends_at;
