-- 000134_pat_project_scope.up.sql
--
-- Project-scoped personal access tokens (#702). A new token is bound to
-- one project and one role (viewer / developer / admin); it can do in
-- that project what a console user with that role can — never more than
-- its creator can right now — and nothing outside it. Tokens that exist
-- when this ships keep project_id / role NULL: legacy account-wide tokens,
-- working exactly as before. No backfill.
-- No explicit BEGIN/COMMIT: golang-migrate wraps the file.

ALTER TABLE public.personal_access_tokens
    ADD COLUMN IF NOT EXISTS project_id UUID REFERENCES public.projects(id) ON DELETE CASCADE,
    ADD COLUMN IF NOT EXISTS role TEXT CHECK (role IN ('viewer', 'developer', 'admin'));

ALTER TABLE public.personal_access_tokens DROP CONSTRAINT IF EXISTS pat_scope_complete;
ALTER TABLE public.personal_access_tokens
    ADD CONSTRAINT pat_scope_complete CHECK ((project_id IS NULL) = (role IS NULL));

CREATE INDEX IF NOT EXISTS ix_pat_project ON public.personal_access_tokens (project_id);
