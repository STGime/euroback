-- 000006_schema_changes.up.sql
-- Track DDL schema changes per project for migration history UI.

BEGIN;

CREATE TABLE public.schema_changes (
    id           UUID        PRIMARY KEY DEFAULT uuid_generate_v4(),
    project_id   UUID        NOT NULL REFERENCES public.projects(id) ON DELETE CASCADE,
    action       TEXT        NOT NULL CHECK (action IN ('create_table', 'drop_table', 'add_column', 'drop_column')),
    table_name   TEXT        NOT NULL,
    column_name  TEXT,
    detail       JSONB       DEFAULT '{}',
    sql_text     TEXT,
    created_at   TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX idx_schema_changes_project_id ON public.schema_changes(project_id);
CREATE INDEX idx_schema_changes_created_at ON public.schema_changes(created_at DESC);

COMMIT;
