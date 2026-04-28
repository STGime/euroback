-- 000042_personal_access_tokens.up.sql
-- Personal Access Tokens (PATs) — long-lived bearer tokens for tooling
-- (MCP server, CLI, CI). Authenticate as the platform user but, by design,
-- never carry the is_superadmin claim even if the underlying user is a
-- superadmin. PATs are for application-tier work, not platform admin.

CREATE TABLE IF NOT EXISTS public.personal_access_tokens (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID        NOT NULL REFERENCES public.platform_users(id) ON DELETE CASCADE,
    name         TEXT        NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    prefix       TEXT        NOT NULL,
    token_hash   TEXT        NOT NULL UNIQUE,
    expires_at   TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_personal_access_tokens_user_id
    ON public.personal_access_tokens (user_id, created_at DESC);
