-- 000111_backup_snapshots_tag.up.sql
--
-- Add optional user-provided tag to on-demand backup snapshots.
--
-- Team-tier customers can now attach a short label when creating an
-- on-demand snapshot ("pre-migration-v2", "before-cleanup",
-- "2026-Q1-close"). The tag is stored here and rendered in the
-- console Backups tab so operators can find + restore a specific
-- snapshot from a longer list.
--
-- Design:
--
--   * Additive column, DEFAULT NULL — existing rows (backfilled from
--     provider + all scheduled snapshots) stay NULL. No data
--     migration.
--   * Length constrained at the handler layer (≤64 chars, printable);
--     no CHECK constraint here — keeps the migration reversible
--     without a rewrite if the max is ever bumped.
--   * Partial index for the "find snapshot by tag" console lookup
--     restricts to non-NULL rows: keeps the index tight (most
--     scheduled snapshots will never carry a tag) and matches the
--     query shape console will emit.

BEGIN;

ALTER TABLE public.backup_snapshots
    ADD COLUMN IF NOT EXISTS tag TEXT;

CREATE INDEX IF NOT EXISTS ix_backup_snapshots_project_tag
    ON public.backup_snapshots (project_id, tag)
    WHERE tag IS NOT NULL;

COMMIT;
