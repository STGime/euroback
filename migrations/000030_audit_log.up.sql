-- Platform-level audit log. Tracks sensitive actions performed by project
-- owners and platform admins: API key regeneration, auth config changes,
-- project deletion, secret management, data exports, etc.
--
-- Intentionally stored in the public schema (not per-tenant) because it
-- tracks platform-level operations that span tenant boundaries (e.g.
-- project creation, key regeneration).

CREATE TABLE public.audit_log (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    project_id  UUID REFERENCES public.projects(id) ON DELETE SET NULL,
    actor_id    UUID REFERENCES public.platform_users(id) ON DELETE SET NULL,
    actor_email TEXT NOT NULL,
    action      TEXT NOT NULL,
    target_type TEXT,
    target_id   TEXT,
    metadata    JSONB DEFAULT '{}'::jsonb,
    ip_address  TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_audit_log_project ON public.audit_log(project_id, created_at DESC);
CREATE INDEX idx_audit_log_actor ON public.audit_log(actor_id, created_at DESC);
CREATE INDEX idx_audit_log_action ON public.audit_log(action, created_at DESC);
