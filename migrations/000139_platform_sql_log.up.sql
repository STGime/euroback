-- 000139_platform_sql_log.up.sql
--
-- Per-project log of the SQL that developers send through the platform:
-- the console SQL editor, the multi-statement endpoint (both also used by
-- the MCP server and the CLI with a personal access token), custom
-- function bodies, custom RLS policy expressions and tenant migrations.
-- One row per statement: who sent it, how (console session or token),
-- the text (capped at 8 KB) and its SHA-256, and what happened (ran,
-- failed, or refused by the platform's checks before it ran).
--
-- The text can contain the project's own data (literals), so the table
-- is readable only on the developer pool (console: project admins) and
-- rows are kept 30 days (gateway cleanup).
--
-- Written by the gateway (INSERT only); read and cleaned up on the
-- developer pool.
-- No explicit BEGIN/COMMIT: golang-migrate wraps the file.

CREATE TABLE IF NOT EXISTS public.platform_sql_log (
    id            BIGSERIAL PRIMARY KEY,
    project_id    UUID NOT NULL REFERENCES public.projects(id) ON DELETE CASCADE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    actor_id      UUID,
    actor_email   TEXT CHECK (length(actor_email) <= 320),
    pat_id        UUID,
    via           TEXT NOT NULL CHECK (via IN ('console', 'token')),
    source        TEXT NOT NULL CHECK (source IN ('sql', 'sql_transaction', 'function', 'policy', 'migration')),
    statement     TEXT NOT NULL CHECK (length(statement) <= 8200),
    statement_len INTEGER NOT NULL,
    sha256        TEXT NOT NULL CHECK (length(sha256) = 64),
    read_only     BOOLEAN NOT NULL DEFAULT false,
    outcome       TEXT NOT NULL CHECK (outcome IN ('ok', 'error', 'refused')),
    detail        TEXT CHECK (length(detail) <= 600),
    duration_ms   INTEGER,
    row_count     INTEGER,
    ip            TEXT CHECK (length(ip) <= 100)
);

CREATE INDEX IF NOT EXISTS ix_platform_sql_log_project_time
    ON public.platform_sql_log (project_id, created_at DESC);
CREATE INDEX IF NOT EXISTS ix_platform_sql_log_time
    ON public.platform_sql_log (created_at);
-- Platform-wide view of refused statements (superadmin).
CREATE INDEX IF NOT EXISTS ix_platform_sql_log_refused
    ON public.platform_sql_log (created_at DESC) WHERE outcome = 'refused';

-- Default privileges (000037) give the gateway full DML and SELECT on the
-- sequence; it only writes.
REVOKE ALL ON public.platform_sql_log FROM eurobase_gateway;
REVOKE ALL ON SEQUENCE public.platform_sql_log_id_seq FROM eurobase_gateway;
GRANT INSERT ON public.platform_sql_log TO eurobase_gateway;
GRANT USAGE ON SEQUENCE public.platform_sql_log_id_seq TO eurobase_gateway;

-- The console reads and the cleanup deletes as eurobase_developer.
GRANT SELECT, DELETE ON public.platform_sql_log TO eurobase_developer;
