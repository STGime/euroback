-- 000135_project_email_log.up.sql
--
-- Per-project log of auth emails (verification, password reset, magic
-- link): sent (and how), skipped (and why), or failed (and why). The
-- auth endpoints answer "OK" in every case so they don't reveal which
-- addresses have accounts; this log is where the project's developers
-- see what actually happened. Recipients are stored masked
-- (p****@example.com). Kept 14 days (gateway cleanup).
--
-- Written by the gateway on SDK auth requests (INSERT only); read and
-- cleaned up on the developer pool — the runtime role can't read other
-- projects' rows.
-- No explicit BEGIN/COMMIT: golang-migrate wraps the file.

CREATE TABLE IF NOT EXISTS public.project_email_log (
    id          BIGSERIAL PRIMARY KEY,
    project_id  UUID NOT NULL REFERENCES public.projects(id) ON DELETE CASCADE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    flow        TEXT NOT NULL CHECK (flow IN ('verification', 'password_reset', 'magic_link')),
    recipient   TEXT NOT NULL CHECK (length(recipient) <= 300),
    outcome     TEXT NOT NULL CHECK (outcome IN ('sent', 'skipped', 'failed')),
    reason      TEXT,
    via         TEXT CHECK (via IN ('platform', 'custom_smtp')),
    message_id  TEXT,
    detail      TEXT CHECK (length(detail) <= 600)
);

CREATE INDEX IF NOT EXISTS ix_project_email_log_project_time
    ON public.project_email_log (project_id, created_at DESC);
CREATE INDEX IF NOT EXISTS ix_project_email_log_time
    ON public.project_email_log (created_at);

-- Default privileges (000037) give the gateway full DML and SELECT on the
-- sequence; it only writes. (The sequence's last_value would otherwise be
-- a platform-wide count of auth email requests.)
REVOKE ALL ON public.project_email_log FROM eurobase_gateway;
REVOKE ALL ON SEQUENCE public.project_email_log_id_seq FROM eurobase_gateway;
GRANT INSERT ON public.project_email_log TO eurobase_gateway;
GRANT USAGE ON SEQUENCE public.project_email_log_id_seq TO eurobase_gateway;

-- The console reads and the cleanup deletes as eurobase_developer.
GRANT SELECT, DELETE ON public.project_email_log TO eurobase_developer;
