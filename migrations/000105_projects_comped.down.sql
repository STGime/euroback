-- 000105_projects_comped.down.sql
--
-- Rollback: drop the index first (partial indexes are cheap to
-- rebuild), then the columns. The downgrade cron's extra guard
-- (comped_until IS NULL OR comped_until < now()) is safe against
-- the column being missing IF the code is also rolled back; if
-- only this migration is reversed the query will error on the
-- missing column, next tick recovers once schema+code agree.

BEGIN;

DROP INDEX IF EXISTS public.idx_projects_comped_until;

ALTER TABLE public.projects
    DROP COLUMN IF EXISTS comped_reason,
    DROP COLUMN IF EXISTS comped_until;

COMMIT;
