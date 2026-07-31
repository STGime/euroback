-- 000080_legacy_pro_grace.down.sql
--
-- Reverses 000080_legacy_pro_grace.up.sql.

BEGIN;

DROP INDEX IF EXISTS idx_projects_legacy_pro_grace_until;

ALTER TABLE public.projects
    DROP COLUMN IF EXISTS legacy_pro_grace_until;

COMMIT;
