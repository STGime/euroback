-- Closes #112. Adds the columns the SDK `eb.functions.schedules` surface needs
-- on top of the existing cron_jobs table, and a unique (project_id, name)
-- index so schedules can be addressed idempotently by their stable name.
--
-- Action type `function` is added so a schedule can target a deployed edge
-- function (Deno runner). The executor maps this to an HTTP invocation
-- against the function runner. `sql` and `rpc` keep their existing semantics.

-- Allow `function` as a third action_type.
ALTER TABLE public.cron_jobs
    DROP CONSTRAINT IF EXISTS cron_jobs_action_type_check;
ALTER TABLE public.cron_jobs
    ADD CONSTRAINT cron_jobs_action_type_check
    CHECK (action_type IN ('sql', 'rpc', 'function'));

-- Schedule metadata. NULL/default-friendly so the executor can run without
-- these fields set — existing rows keep working.
ALTER TABLE public.cron_jobs
    ADD COLUMN IF NOT EXISTS timezone    TEXT NOT NULL DEFAULT 'UTC',
    ADD COLUMN IF NOT EXISTS description TEXT,
    ADD COLUMN IF NOT EXISTS payload     JSONB,
    ADD COLUMN IF NOT EXISTS headers     JSONB;

-- Idempotent provisioning by name: `eb.functions.schedules.create('foo', ...)`
-- needs (project_id, name) to be unique so a second call with the same name
-- can be detected and rejected as a conflict.
CREATE UNIQUE INDEX IF NOT EXISTS cron_jobs_project_name_uq
    ON public.cron_jobs (project_id, name);
