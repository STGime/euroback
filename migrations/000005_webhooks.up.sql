-- 000005_webhooks.up.sql
-- Customer-configurable webhook endpoints and delivery tracking.

BEGIN;

CREATE TABLE public.webhooks (
    id           UUID        PRIMARY KEY DEFAULT uuid_generate_v4(),
    project_id   UUID        NOT NULL REFERENCES public.projects(id) ON DELETE CASCADE,
    url          TEXT        NOT NULL,
    events       TEXT[]      NOT NULL DEFAULT '{}',
    secret       TEXT        NOT NULL,
    enabled      BOOLEAN     DEFAULT true,
    description  TEXT        DEFAULT '',
    created_at   TIMESTAMPTZ DEFAULT now()
);

CREATE TABLE public.webhook_deliveries (
    id           UUID        PRIMARY KEY DEFAULT uuid_generate_v4(),
    webhook_id   UUID        NOT NULL REFERENCES public.webhooks(id) ON DELETE CASCADE,
    event        TEXT        NOT NULL,
    payload      JSONB       NOT NULL DEFAULT '{}',
    status_code  INTEGER,
    response     TEXT,
    attempts     INTEGER     DEFAULT 1,
    success      BOOLEAN     DEFAULT false,
    created_at   TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX idx_webhooks_project_id ON public.webhooks(project_id);
CREATE INDEX idx_webhook_deliveries_webhook_id ON public.webhook_deliveries(webhook_id);
CREATE INDEX idx_webhook_deliveries_created_at ON public.webhook_deliveries(created_at DESC);

COMMIT;
