-- 000083_project_databases.up.sql
--
-- Team tier M1: dedicated Postgres instance per project.
--
-- Every project on the base tiers (Free / Pro) shares one Postgres
-- cluster with per-tenant schema isolation + RLS (see CLAUDE.md).
-- Team-tier (and Legal-Team-tier) projects get their own instance,
-- provisioned via internal/dbprovider — Scaleway managed PG (fr-par)
-- is the initial implementation; the interface allows for other
-- EU-headquartered providers (Aiven FI, OVHcloud FR, CleverCloud FR,
-- self-hosted) later.
--
-- Design notes:
--
--  * Password is stored encrypted inline (ciphertext + nonce +
--    key_version) instead of via a vault_secrets reference. The
--    vault package is per-tenant-scoped (RLS on tenant_*.vault_secrets)
--    so it doesn't fit a platform-owned credential. AES-256-GCM with
--    a master key from VAULT_ENCRYPTION_KEY (already required by the
--    vault package — no new secret).
--
--  * `superseded_by` FK enables the two-instance restore pattern from
--    M3: when a Team project is restored, a new instance is
--    provisioned, then the OLD instance's row is marked
--    deleted_at=now() with superseded_by pointing at the new row.
--    Deprovision worker sweeps 7 days later — that window is the
--    rollback safety net.
--
--  * Unique partial index ensures **exactly one live instance** per
--    project. States that count as "live": provisioning, active,
--    restoring. During cutover both rows briefly overlap, but the
--    OLD one has deleted_at set at the instant of the swap so it
--    drops out of the index immediately.

BEGIN;

CREATE TABLE public.project_databases (
    id                    UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    -- ON DELETE RESTRICT (not CASCADE) — every row here owns a
    -- billed managed-PG instance at the provider. Cascading the
    -- delete would silently drop the row before the deprovision
    -- worker could tear down the underlying instance, leaving an
    -- orphan running (~€50-500/mo). The FK becomes the enforcement
    -- point: `DELETE FROM projects` fails until the caller walks
    -- MarkDeleted → 7-day window → DeprovisionTeamDatabaseWorker.
    project_id            UUID        NOT NULL REFERENCES public.projects(id) ON DELETE RESTRICT,

    -- Provider identity. Restricted at application layer to the
    -- EU-only allow-list in internal/dbprovider/registry.go — no CHECK
    -- constraint here because adding a new provider must not require
    -- a migration.
    provider              TEXT        NOT NULL,
    provider_instance_id  TEXT        NOT NULL,

    -- Connection endpoint. Host may change on restore cutover; the
    -- per-project pgxpool cache in the gateway is invalidated when
    -- state flips to 'active' on a new row.
    host                  TEXT        NOT NULL,
    port                  INT         NOT NULL,
    database_name         TEXT        NOT NULL,
    username              TEXT        NOT NULL,

    -- Password sealed with the platform master key (VAULT_ENCRYPTION_KEY
    -- via internal/dbprovider/cipher.go). Never persisted plaintext.
    password_ciphertext   BYTEA       NOT NULL,
    password_nonce        BYTEA       NOT NULL,
    password_key_version  SMALLINT    NOT NULL DEFAULT 1,

    region                TEXT        NOT NULL DEFAULT 'fr-par',

    -- Lifecycle state. Application-enforced enum: provisioning |
    -- active | restoring | failed | deleting. No CHECK because we may
    -- add states (e.g. 'migrating' for cross-provider moves) without
    -- wanting a schema round-trip.
    state                 TEXT        NOT NULL,

    -- Two-instance restore linkage.
    superseded_by         UUID        REFERENCES public.project_databases(id) ON DELETE SET NULL,

    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Non-NULL deleted_at means the deprovision worker will delete
    -- the row + destroy the underlying instance after the 7-day
    -- rollback window. See internal/workers/deprovision_team_db.go.
    deleted_at            TIMESTAMPTZ
);

-- One live instance per project. "Live" = anything in the active
-- pipeline; deleted_at IS NOT NULL rows drop out of the index so a
-- fresh restore can insert a new live row alongside the old one
-- during the cutover window.
CREATE UNIQUE INDEX ux_project_databases_live_one_per_project
    ON public.project_databases(project_id)
    WHERE state IN ('provisioning', 'active', 'restoring')
      AND deleted_at IS NULL;

-- Deprovision sweeper needs a cheap "who's ready to delete" query.
CREATE INDEX ix_project_databases_deleted_at
    ON public.project_databases(deleted_at)
    WHERE deleted_at IS NOT NULL;

-- Gateway routing lookup — every request for a Team project reads
-- (host, port, ...) by project_id, filtered to the live row.
CREATE INDEX ix_project_databases_project_live
    ON public.project_databases(project_id)
    WHERE state = 'active' AND deleted_at IS NULL;

-- Keep updated_at fresh automatically. Mirror the pattern used on
-- projects (000001_platform_schema).
CREATE OR REPLACE FUNCTION public.project_databases_touch_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- Convention (per CLAUDE.md #217 sweep): non-secdef helpers created
-- by migrator in schema public still get PUBLIC EXECUTE by default,
-- so revoke it to keep the surface minimal. Only the migrator itself
-- invokes this via the trigger below.
REVOKE EXECUTE ON FUNCTION public.project_databases_touch_updated_at() FROM PUBLIC;

CREATE TRIGGER trg_project_databases_touch_updated_at
    BEFORE UPDATE ON public.project_databases
    FOR EACH ROW
    EXECUTE FUNCTION public.project_databases_touch_updated_at();

-- Read + insert + update to the runtime + developer roles. No delete
-- from runtime — instance deletion is a worker + platform-admin
-- operation, not a request-path action.
GRANT SELECT, INSERT, UPDATE ON public.project_databases TO eurobase_gateway;
GRANT SELECT, INSERT, UPDATE ON public.project_databases TO eurobase_developer;

COMMIT;
