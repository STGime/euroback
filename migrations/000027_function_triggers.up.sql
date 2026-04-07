-- Function triggers: link edge functions to database table events.
CREATE TABLE public.function_triggers (
    id           UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    function_id  UUID NOT NULL REFERENCES edge_functions(id) ON DELETE CASCADE,
    project_id   UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    table_name   TEXT NOT NULL,
    events       TEXT[] NOT NULL,  -- '{INSERT,UPDATE,DELETE}'
    enabled      BOOLEAN DEFAULT true,
    created_at   TIMESTAMPTZ DEFAULT now(),
    UNIQUE(function_id, table_name)
);

CREATE INDEX idx_fn_triggers_project ON function_triggers(project_id);
CREATE INDEX idx_fn_triggers_function ON function_triggers(function_id);
