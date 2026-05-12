DROP INDEX IF EXISTS cron_jobs_project_name_uq;

ALTER TABLE public.cron_jobs
    DROP COLUMN IF EXISTS headers,
    DROP COLUMN IF EXISTS payload,
    DROP COLUMN IF EXISTS description,
    DROP COLUMN IF EXISTS timezone;

ALTER TABLE public.cron_jobs
    DROP CONSTRAINT IF EXISTS cron_jobs_action_type_check;
ALTER TABLE public.cron_jobs
    ADD CONSTRAINT cron_jobs_action_type_check
    CHECK (action_type IN ('sql', 'rpc'));
