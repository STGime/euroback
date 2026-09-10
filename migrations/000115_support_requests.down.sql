-- Reverse of 000115_support_requests.up.sql.
DROP INDEX IF EXISTS public.ix_support_requests_by_user;
DROP INDEX IF EXISTS public.ix_support_requests_unresolved_recent;
DROP TABLE IF EXISTS public.support_requests;
