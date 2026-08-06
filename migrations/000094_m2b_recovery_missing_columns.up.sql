-- 000094_m2b_recovery_missing_columns.up.sql
--
-- Recovery migration for the M2b (legal-team + compliance pack)
-- schema, which was SILENTLY SKIPPED on prod due to out-of-order
-- migration numbering.
--
-- Sequence of events:
--   1. PR #334 (M3 backup/PITR) merged first → migrations 090/091/092
--      applied → schema_migrations.version = 92.
--   2. PR #332 (M2b) merged after → migrations 087/088/089 shipped
--      in the bundle.
--   3. golang-migrate's `up` only applies migrations with version >
--      current — so 087/088/089 were considered already-applied
--      and silently skipped.
--   4. Migrate Job succeeded trivially in 10s ("condition met").
--   5. M2b code deployed referencing `legal_team_beta_access` etc.
--      Column doesn't exist → every profile fetch returns 500 →
--      superadmin gate can't verify → admin routes inaccessible.
--
-- Root-cause bug: migration numbers assigned to a PR without
-- rebasing against main's current head. Follow-up: CI check that
-- rejects a PR whose migration versions overlap or precede main's
-- current head. Tracked as a separate task.
--
-- This migration is a forward-only recovery. Every piece is idempotent
-- (IF NOT EXISTS, ON CONFLICT DO NOTHING, DO $$ IF NOT EXISTS $$)
-- so re-running against a hypothetical prod-was-fine state is a no-op.
--
-- Also fixes 000089's latent IMMUTABLE bug: its
-- `WHERE expires_at > now()` partial-index predicate is the same
-- class of bug M3's 000090 hit (predicates must reference only
-- IMMUTABLE functions; now() is STABLE). Would have fired on a fresh
-- setup but is a latent issue only because 000089 never ran anywhere.

BEGIN;

-- ── 087_legal_team_tier ────────────────────────────────────────────

ALTER TABLE public.platform_users
    ADD COLUMN IF NOT EXISTS legal_team_beta_access     BOOLEAN     NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS legal_team_beta_granted_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS legal_team_beta_granted_by UUID        REFERENCES public.platform_users(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS ix_platform_users_legal_team_beta
    ON public.platform_users(legal_team_beta_granted_at DESC)
    WHERE legal_team_beta_access = true;

-- plan_limits row — ON CONFLICT DO NOTHING makes it safe if it
-- somehow already got in (e.g. someone hand-inserted during triage).
INSERT INTO public.plan_limits (
    plan, db_size_mb, storage_mb, bandwidth_mb, mau_limit,
    rate_limit_rps, ws_connections, upload_size_mb, webhook_limit,
    project_limit, log_retention_days, custom_templates,
    edge_function_limit, dsar_console_ui,
    custom_domain, byo_smtp, quota_alerts,
    price_cents, dedicated_db, pitr_days, backup_retention_days,
    audit_log_retention_days
) VALUES (
    'legal_team',
    102400, 512000, 1048576, 1000000,
    5000, 50000, 500, 0,
    50, 90, true,
    250, true,
    true, true, true,
    NULL, true, 7, 30,
    3650
)
ON CONFLICT (plan) DO NOTHING;

-- ── 088_staff_secrecy ──────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS public.staff_secrecy_declarations (
    id                    UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    employee_name         TEXT        NOT NULL,
    role                  TEXT        NOT NULL,
    declaration_signed_at TIMESTAMPTZ NOT NULL,
    declaration_hash      TEXT        NOT NULL CHECK (declaration_hash ~ '^[0-9a-f]{64}$'),
    revoked_at            TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS ix_staff_secrecy_current
    ON public.staff_secrecy_declarations(declaration_signed_at DESC)
    WHERE revoked_at IS NULL;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'eurobase_gateway') THEN
        EXECUTE 'GRANT SELECT ON public.staff_secrecy_declarations TO eurobase_gateway';
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'eurobase_developer') THEN
        EXECUTE 'GRANT SELECT, INSERT, UPDATE ON public.staff_secrecy_declarations TO eurobase_developer';
    END IF;
END $$;

-- ── 089_retention_holds ────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS public.retention_holds (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id   UUID        NOT NULL REFERENCES public.projects(id) ON DELETE CASCADE,
    target_type  TEXT        NOT NULL CHECK (target_type IN ('row','object','table')),
    target_ref   JSONB       NOT NULL,
    legal_basis  TEXT        NOT NULL,
    expires_at   TIMESTAMPTZ NOT NULL,
    created_by   UUID        REFERENCES public.platform_users(id) ON DELETE SET NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- NB: dropped 000089's original `WHERE expires_at > now()` predicate
-- on ix_retention_holds_lookup — same class as M3's 090 IMMUTABLE
-- bug (now() is STABLE, not IMMUTABLE, and can't be used in an
-- index predicate). Plain index; query-time filter on expires_at
-- covers the "hide expired" case.
CREATE INDEX IF NOT EXISTS ix_retention_holds_lookup
    ON public.retention_holds(project_id, target_type);

CREATE INDEX IF NOT EXISTS ix_retention_holds_targetref
    ON public.retention_holds USING GIN (target_ref jsonb_path_ops);

-- Sweeper index — expires_at is NOT NULL, so plain index covers
-- the sweep. Dropped 000089's `WHERE expires_at > '1970-01-01'`
-- fake-guard (was probably added to work around the earlier IMMUTABLE
-- issue on the sibling index; not needed here).
CREATE INDEX IF NOT EXISTS ix_retention_holds_expiry
    ON public.retention_holds(expires_at);

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'eurobase_gateway') THEN
        EXECUTE 'GRANT SELECT, INSERT, DELETE ON public.retention_holds TO eurobase_gateway';
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'eurobase_developer') THEN
        EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON public.retention_holds TO eurobase_developer';
    END IF;
END $$;

COMMIT;
