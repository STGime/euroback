-- 000035_oauth_states.up.sql
-- Server-side storage for OAuth CSRF state tokens. Each state is single-use
-- and expires after 10 minutes. The row binds the opaque state value to the
-- project + client redirect URL the user began the flow with.

CREATE TABLE IF NOT EXISTS public.oauth_states (
    state         TEXT PRIMARY KEY,
    project_id    UUID NOT NULL REFERENCES public.projects(id) ON DELETE CASCADE,
    provider      TEXT NOT NULL,
    redirect_url  TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at    TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_oauth_states_expires ON public.oauth_states(expires_at);
