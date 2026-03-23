CREATE TABLE public.request_logs (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id   UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    method       TEXT NOT NULL,
    path         TEXT NOT NULL,
    status_code  INTEGER NOT NULL,
    latency_ms   INTEGER NOT NULL,
    ip_address   TEXT,
    user_agent   TEXT,
    created_at   TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX idx_request_logs_project_time ON request_logs(project_id, created_at DESC);
