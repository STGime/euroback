-- 000111_backup_snapshots_tag.down.sql

BEGIN;

DROP INDEX IF EXISTS public.ix_backup_snapshots_project_tag;

ALTER TABLE public.backup_snapshots
    DROP COLUMN IF EXISTS tag;

COMMIT;
