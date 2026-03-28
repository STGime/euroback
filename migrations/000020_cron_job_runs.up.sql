CREATE TABLE public.cron_job_runs (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    job_id      UUID NOT NULL REFERENCES cron_jobs(id) ON DELETE CASCADE,
    project_id  UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    started_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ,
    duration_ms INT,
    status      TEXT NOT NULL CHECK (status IN ('running', 'success', 'error')),
    result      TEXT,
    error       TEXT,
    created_at  TIMESTAMPTZ DEFAULT now()
);
CREATE INDEX idx_cron_runs_job ON public.cron_job_runs(job_id, started_at DESC);
