-- 000107_project_databases_backup_schedule.up.sql
--
-- Team-tier M3 follow-up (#457). Tracks whether we've explicitly
-- configured Scaleway's backup schedule on the instance — as
-- opposed to relying on Scaleway's undocumented defaults.
--
-- Motivation:
--   Prior to #457, provisioning a Team/Legal-Team instance NEVER
--   called Scaleway's set-backup-schedule endpoint. Scaleway then
--   applied its own default retention (undocumented, node-type-
--   dependent), so our `plan_limits.backup_retention_days=30`
--   promise was aspirational. The reconcile sweeper (added in the
--   same PR as this migration) needs a per-row flag to know which
--   instances still need a set-schedule call:
--
--     * NULL       — never applied; sweeper enqueues a reconcile
--                    job. Covers both (a) instances provisioned
--                    before #457 lands, and (b) new provisions
--                    where the inline set-schedule call failed
--                    (e.g. Scaleway warmup rejection).
--     * timestamp  — applied at this time; sweeper skips.
--
--   The column is set to now() by the provision worker AND by the
--   reconcile worker on successful SetBackupSchedule. It is NOT
--   cleared on plan changes — plan_limits.backup_retention_days
--   would need to change first (rare), and we'd handle that with a
--   separate migration + one-off backfill rather than a per-row
--   TTL that hammers Scaleway hourly.
--
-- Design:
--   * Nullable (NULL = never applied) is the natural signal.
--   * Partial index on WHERE column IS NULL matches the sweeper's
--     WHERE filter exactly; keeps the scan tiny once the backlog
--     drains (~all rows will have a timestamp).
--   * No CHECK constraint — the value is set by the worker, not
--     user-supplied, so trust the writer.

BEGIN;

ALTER TABLE public.project_databases
    ADD COLUMN IF NOT EXISTS backup_schedule_applied_at TIMESTAMPTZ;

-- Partial index: only unapplied rows. After the first sweep drains
-- the backlog, this index size approaches zero — cheap to keep and
-- makes the sweeper's `WHERE backup_schedule_applied_at IS NULL`
-- an index-only scan.
CREATE INDEX IF NOT EXISTS idx_project_databases_backup_schedule_unapplied
    ON public.project_databases (id)
    WHERE backup_schedule_applied_at IS NULL AND state = 'active' AND deleted_at IS NULL;

COMMIT;
