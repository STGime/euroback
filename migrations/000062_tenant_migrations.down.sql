-- Drops tenant migration bookkeeping. Applied tenant-schema changes are
-- NOT rolled back — only the record of them is lost.
DROP TABLE IF EXISTS public.tenant_migrations;
