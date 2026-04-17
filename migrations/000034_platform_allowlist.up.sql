-- 000034_platform_allowlist.up.sql
-- Closed-beta signup gate: only emails in this table can register.
-- Set ALLOW_PUBLIC_SIGNUP=true to bypass (open registration).

CREATE TABLE IF NOT EXISTS public.platform_allowlist (
    email      TEXT PRIMARY KEY,
    note       TEXT,          -- e.g. "beta tester", "investor"
    created_at TIMESTAMPTZ DEFAULT now()
);
