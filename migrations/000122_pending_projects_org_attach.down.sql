-- 000122_pending_projects_org_attach.down.sql
BEGIN;

ALTER TABLE public.pending_projects
    DROP COLUMN IF EXISTS org_id_explicit,
    DROP COLUMN IF EXISTS org_id;

COMMIT;
