-- 000090_backup_snapshots.up.sql
--
-- Team-tier M3 (backup + PITR). Caches provider-side snapshot
-- metadata so the console's Backups tab can list snapshots without
-- a live Scaleway API call on every page load. Refreshed by a cron
-- that runs Provider.ListSnapshots per Team project every 6h.
--
-- Design:
--
--   * Cache table, not source-of-truth — Scaleway holds the real
--     backup files. If a row is missing here but exists there
--     (fresh Scaleway automatic backup our sync hasn't caught),
--     the on-demand refresh endpoint pulls it in on demand.
--   * `kind` discriminates provider-scheduled ('scheduled')
--     from user-triggered ('ondemand'). Enum enforced at the
--     application layer (application code owns these strings).
--   * `expires_at` is authoritative for retention — a snapshot
--     past its expiry is invisible to the list endpoint even
--     if still present on Scaleway (defence-in-depth against
--     provider quirks). The prune cron uses this same column.
--   * Numbered 000090 not 000087 to reserve 087-089 for the
--     in-flight M2b PR (#332) which uses those numbers.

BEGIN;

CREATE TABLE public.backup_snapshots (
    id                     UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id             UUID        NOT NULL REFERENCES public.projects(id) ON DELETE CASCADE,
    project_database_id    UUID        NOT NULL REFERENCES public.project_databases(id) ON DELETE CASCADE,

    -- Provider-assigned snapshot identifier. Distinct from `id` so
    -- the provider can rename or reissue without breaking our row.
    provider_snapshot_id   TEXT        NOT NULL,

    -- Human-readable name — the on-demand path uses
    -- `eurobase-ondemand-<hex>`; scheduled uses whatever the
    -- provider chose (Scaleway defaults to `autobackup_<date>`).
    -- Kept for display; the classifier uses `kind` not the name.
    name                   TEXT        NOT NULL,

    size_mb                BIGINT      NOT NULL DEFAULT 0,
    kind                   TEXT        NOT NULL,
    created_at             TIMESTAMPTZ NOT NULL,
    expires_at             TIMESTAMPTZ NOT NULL,

    -- When this row was last refreshed from the provider — lets
    -- the cron detect stale entries.
    synced_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Uniqueness: one row per (project, provider_snapshot_id). Prevents
-- duplicate rows from a poorly-timed refresh + on-demand create.
CREATE UNIQUE INDEX ux_backup_snapshots_provider_id
    ON public.backup_snapshots(project_id, provider_snapshot_id);

-- Console-list query filter — Team-tier projects fetch their own
-- snapshot list ordered newest-first. Query-time filter on
-- expires_at handles the "hide expired" behaviour; a partial
-- predicate using now() is rejected by Postgres (index predicates
-- must reference only IMMUTABLE functions, and now() is STABLE).
CREATE INDEX ix_backup_snapshots_project_created
    ON public.backup_snapshots(project_id, created_at DESC);

-- Prune sweeper — daily cron deletes expired rows.
-- expires_at is NOT NULL, so the plain index covers the sweep.
CREATE INDEX ix_backup_snapshots_expiry
    ON public.backup_snapshots(expires_at);

GRANT SELECT, INSERT, UPDATE, DELETE ON public.backup_snapshots TO eurobase_gateway;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.backup_snapshots TO eurobase_developer;

COMMIT;
