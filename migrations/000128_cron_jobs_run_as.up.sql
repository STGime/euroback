-- 000128_cron_jobs_run_as.up.sql
--
-- #643: RLS identity for cron sql / rpc actions.
--   'service' — the job's transaction sets app.end_user_role = 'service'
--               (is_service_role() = true), like a user-less edge-function
--               invocation, which cron `function` jobs already are.
--   'none'    — no end-user context (the behaviour before this migration).
--
-- Existing jobs keep 'none' so none of them starts touching rows its
-- policies hid before; new jobs default to 'service'. Either way the job
-- still runs as the tenant's own `<schema>_func` login (#642), so
-- 'service' only affects policies inside the tenant's own schema.
--
-- No explicit BEGIN/COMMIT: golang-migrate wraps each .up.sql in its own tx.

ALTER TABLE public.cron_jobs
    ADD COLUMN IF NOT EXISTS run_as text NOT NULL DEFAULT 'none'
    CONSTRAINT cron_jobs_run_as_check CHECK (run_as IN ('none', 'service'));

ALTER TABLE public.cron_jobs ALTER COLUMN run_as SET DEFAULT 'service';
