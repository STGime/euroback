-- Edge function version history for rollback support.
CREATE TABLE public.edge_function_versions (
    id           UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    function_id  UUID NOT NULL REFERENCES edge_functions(id) ON DELETE CASCADE,
    version      INTEGER NOT NULL,
    code         TEXT NOT NULL,
    created_at   TIMESTAMPTZ DEFAULT now(),
    UNIQUE(function_id, version)
);

CREATE INDEX idx_fn_versions_function ON edge_function_versions(function_id, version DESC);
