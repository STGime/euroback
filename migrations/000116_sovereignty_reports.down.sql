-- Reverse of 000116_sovereignty_reports.up.sql.
DROP INDEX IF EXISTS public.ix_sovereignty_leads_stage;
DROP INDEX IF EXISTS public.ux_sovereignty_leads_email_campaign;
DROP TABLE IF EXISTS public.sovereignty_leads;
DROP INDEX IF EXISTS public.ix_sovereignty_reports_created;
DROP INDEX IF EXISTS public.ix_sovereignty_reports_hash;
DROP TABLE IF EXISTS public.sovereignty_reports;
