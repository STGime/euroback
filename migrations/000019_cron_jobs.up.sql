CREATE TABLE public.cron_jobs (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    project_id  UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    schedule    TEXT NOT NULL,              -- cron expression e.g. "*/5 * * * *"
    action_type TEXT NOT NULL CHECK (action_type IN ('sql', 'rpc')),
    action      TEXT NOT NULL,              -- SQL statement or function name
    enabled     BOOLEAN DEFAULT true,
    last_run_at TIMESTAMPTZ,
    last_error  TEXT,
    run_count   INT DEFAULT 0,
    created_at  TIMESTAMPTZ DEFAULT now(),
    updated_at  TIMESTAMPTZ DEFAULT now()
);
CREATE INDEX idx_cron_jobs_project ON public.cron_jobs(project_id);
CREATE INDEX idx_cron_jobs_enabled ON public.cron_jobs(enabled) WHERE enabled = true;
