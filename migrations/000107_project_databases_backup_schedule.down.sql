-- 000107_project_databases_backup_schedule.down.sql
--
-- Rollback drops the index first (partial indexes are cheap to
-- rebuild) then the column. The reconcile sweeper's WHERE clause
-- would error if the column vanished while the code still expected
-- it — coordinate rollbacks: code first, then this migration.

BEGIN;

DROP INDEX IF EXISTS public.idx_project_databases_backup_schedule_unapplied;

ALTER TABLE public.project_databases
    DROP COLUMN IF EXISTS backup_schedule_applied_at;

COMMIT;
