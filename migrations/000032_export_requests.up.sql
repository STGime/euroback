-- DSAR export request tracking table.
-- Shared by both tenant-level (full project) and end-user-level exports.
CREATE TABLE public.export_requests (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id       UUID NOT NULL REFERENCES public.projects(id) ON DELETE CASCADE,
    user_id          UUID,
    status           TEXT NOT NULL DEFAULT 'pending'
                         CHECK (status IN ('pending', 'running', 'completed', 'failed')),
    format           TEXT NOT NULL DEFAULT 'json'
                         CHECK (format IN ('json', 'csv')),
    s3_key           TEXT,
    file_size        BIGINT,
    error            TEXT,
    requested_by     UUID NOT NULL,
    requested_by_type TEXT NOT NULL DEFAULT 'platform'
                         CHECK (requested_by_type IN ('platform', 'enduser')),
    started_at       TIMESTAMPTZ,
    completed_at     TIMESTAMPTZ,
    expires_at       TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_export_requests_project ON public.export_requests(project_id, created_at DESC);
CREATE INDEX idx_export_requests_status ON public.export_requests(status)
    WHERE status IN ('pending', 'running');
