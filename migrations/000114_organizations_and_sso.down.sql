-- 000114_organizations_and_sso.down.sql
--
-- Drop in reverse dependency order: projects.org_id first (no other
-- tables reference it), then org_members (references organizations),
-- then organizations itself.

ALTER TABLE public.projects DROP COLUMN IF EXISTS org_id;

DROP TABLE IF EXISTS public.org_members;
DROP TABLE IF EXISTS public.organizations;
