-- Tracks which usage-alert thresholds have already been emailed to a project
-- owner, so a daily background job doesn't re-spam the same warning. When
-- usage drops back below a threshold the corresponding row is deleted,
-- allowing the alert to fire again on the next breach.

CREATE TABLE public.usage_alerts_sent (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    project_id  UUID NOT NULL REFERENCES public.projects(id) ON DELETE CASCADE,
    dimension   TEXT NOT NULL CHECK (dimension IN ('db_size', 'storage', 'mau', 'edge_functions', 'webhooks')),
    threshold   INTEGER NOT NULL CHECK (threshold IN (80, 90, 100)),
    sent_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, dimension, threshold)
);

CREATE INDEX idx_usage_alerts_sent_project ON public.usage_alerts_sent(project_id);
