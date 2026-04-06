-- Edge Functions: serverless TypeScript/JavaScript function execution.

-- Add edge function limit to plan_limits.
ALTER TABLE public.plan_limits ADD COLUMN edge_function_limit INT NOT NULL DEFAULT 3;
UPDATE public.plan_limits SET edge_function_limit = 3 WHERE plan = 'free';
UPDATE public.plan_limits SET edge_function_limit = 25 WHERE plan = 'pro';

-- Function code storage.
CREATE TABLE public.edge_functions (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    project_id  UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    code        TEXT NOT NULL,
    verify_jwt  BOOLEAN NOT NULL DEFAULT true,
    env_vars    JSONB DEFAULT '{}',
    status      TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    version     INTEGER NOT NULL DEFAULT 1,
    created_at  TIMESTAMPTZ DEFAULT now(),
    updated_at  TIMESTAMPTZ DEFAULT now(),
    UNIQUE(project_id, name)
);

CREATE INDEX idx_edge_functions_project ON public.edge_functions(project_id);

-- Execution logs.
CREATE TABLE public.edge_function_logs (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    function_id     UUID NOT NULL REFERENCES edge_functions(id) ON DELETE CASCADE,
    project_id      UUID NOT NULL,
    status          INTEGER NOT NULL,
    duration_ms     INTEGER NOT NULL,
    error           TEXT,
    request_method  TEXT NOT NULL,
    created_at      TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX idx_edge_fn_logs_fn ON public.edge_function_logs(function_id, created_at DESC);
CREATE INDEX idx_edge_fn_logs_project ON public.edge_function_logs(project_id, created_at DESC);
