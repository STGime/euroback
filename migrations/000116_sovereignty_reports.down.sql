-- Reverse of 000116_sovereignty_reports.up.sql.
DROP INDEX IF EXISTS public.ix_sovereignty_leads_stage;
DROP TABLE IF EXISTS public.sovereignty_leads;
DROP INDEX IF EXISTS public.ix_sovereignty_reports_created;
DROP INDEX IF EXISTS public.ix_sovereignty_reports_hash;
DROP TABLE IF EXISTS public.sovereignty_reports;
-- Leave the citext extension in place — other migrations may
-- rely on it.
