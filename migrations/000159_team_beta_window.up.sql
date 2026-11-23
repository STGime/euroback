-- Team beta window: Team / Legal Team projects created from this release on
-- get a free beta of 14 days from creation (set by CreateProject). Projects
-- that already exist keep NULL and their current terms.
--
-- beta_ends_at      end of the beta; NULL = no limit
-- beta_reminded_at  when the owner was told the end is near (once per window)
-- beta_expired_at   when the worker suspended the project because the beta
--                   ended; an admin extension clears it and lifts that
--                   suspension
ALTER TABLE public.projects
    ADD COLUMN IF NOT EXISTS beta_ends_at     TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS beta_reminded_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS beta_expired_at  TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS ix_projects_beta_ends_at
    ON public.projects (beta_ends_at) WHERE beta_ends_at IS NOT NULL;
