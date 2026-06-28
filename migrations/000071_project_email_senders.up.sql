-- 000071_project_email_senders.up.sql
--
-- #235 Part 1: BYO custom SMTP. Lets a project send transactional + auth
-- email through their own SMTP provider instead of the shared platform
-- TEM sender. This unblocks the per-project EmailsPerHour ceiling from
-- #227 (a project that outgrows the platform default just plugs in
-- their own provider).
--
-- Why a dedicated table, not auth_config JSON
-- ===========================================
-- The SMTP password MUST be encrypted at rest with the per-tenant HKDF
-- key (same pattern as edge_functions.env_vars sealed in #206). A JSONB
-- sub-object on projects.auth_config can't model "this column is
-- sealed bytes, those are plaintext" without each read/write site
-- doing custom serialization. A dedicated table with the
-- (blob, nonce, key_version) trio + CHECK constraint mirrors what
-- 000067 did for env_vars — the read/write codepaths are already
-- proven.
--
-- The non-secret config (host, port, from_email, ...) lives in the same
-- table so a SELECT returns either everything or nothing — a project
-- either has a sender or doesn't.
--
-- verified_at / last_error / last_error_at
-- ========================================
-- The console flow is "set credentials → send test → see the result".
-- A successful test bumps verified_at; a failed test records
-- last_error + last_error_at. The send-path then refuses to use an
-- unverified sender so a misconfigured SMTP fails loudly at setup,
-- not silently at first signup. (This is the Supabase behaviour.)
--
-- No explicit BEGIN/COMMIT: golang-migrate wraps each .up.sql in its own tx.

CREATE TABLE public.project_email_senders (
    project_id           UUID PRIMARY KEY REFERENCES public.projects(id) ON DELETE CASCADE,

    host                 TEXT        NOT NULL,
    port                 INT         NOT NULL CHECK (port BETWEEN 1 AND 65535),
    username             TEXT,                                       -- nullable for providers that send by API key (rare in pure SMTP)
    from_email           TEXT        NOT NULL,
    from_name            TEXT,
    encryption           TEXT        NOT NULL CHECK (encryption IN ('starttls', 'tls', 'none')),

    -- Sealed password (AES-256-GCM via per-tenant HKDF key, schema_name
    -- as salt — mirrors edge_functions sealing in 000067). All three
    -- columns NULL if the provider doesn't require a password (rare);
    -- the all-or-nothing CHECK keeps the trio honest.
    password_blob        BYTEA,
    password_nonce       BYTEA,
    password_key_version SMALLINT,

    verified_at          TIMESTAMPTZ,
    last_error           TEXT,
    last_error_at        TIMESTAMPTZ,

    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT password_all_or_nothing CHECK (
        (password_blob IS NULL AND password_nonce IS NULL AND password_key_version IS NULL)
     OR (password_blob IS NOT NULL AND password_nonce IS NOT NULL AND password_key_version IS NOT NULL)
    ),

    CONSTRAINT from_email_shape CHECK (
        from_email LIKE '%@%' AND length(from_email) BETWEEN 3 AND 320
    )
);

COMMENT ON TABLE public.project_email_senders IS
  '#235 Part 1: per-project SMTP sender. Sealed password mirrors #206 env_vars pattern. Verified via test-send before the send-path will use it.';

COMMENT ON COLUMN public.project_email_senders.verified_at IS
  'Last successful test-send. The send-path refuses to use the sender until this is non-null, so a misconfigured SMTP fails loudly at setup not silently at first signup.';

-- Touch updated_at automatically on any change so the console can show
-- "last edited 5m ago" without a separate audit lookup.
CREATE OR REPLACE FUNCTION public.touch_project_email_senders_updated_at()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at := now();
    RETURN NEW;
END$$;

ALTER FUNCTION public.touch_project_email_senders_updated_at() OWNER TO eurobase_migrator;
-- CONVENTION (CLAUDE.md): every new helper function in public REVOKE
-- EXECUTE FROM PUBLIC. This one is invoked by the trigger as its owner
-- (migrator), so no role outside the migration ever calls it directly.
REVOKE EXECUTE ON FUNCTION public.touch_project_email_senders_updated_at() FROM PUBLIC;

CREATE TRIGGER touch_project_email_senders_updated_at
BEFORE UPDATE ON public.project_email_senders
FOR EACH ROW EXECUTE FUNCTION public.touch_project_email_senders_updated_at();

-- ── Grants ─────────────────────────────────────────────────────────────
-- The gateway is the only runtime role that reads/writes this table —
-- the function runner has no business reaching email config. Migration
-- 000037 already auto-grants gateway full DML on every migrator-created
-- public.* table, so this is documentation-grade; the explicit grant
-- below is belt-and-braces and survives a future blanket revoke.
GRANT SELECT, INSERT, UPDATE, DELETE ON public.project_email_senders TO eurobase_gateway;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'eurobase_developer') THEN
        EXECUTE 'GRANT  SELECT                ON public.project_email_senders TO   eurobase_developer';
    END IF;
END$$;
