BEGIN;
DROP INDEX IF EXISTS public.idx_legal_acceptances_document;
DROP INDEX IF EXISTS public.idx_legal_acceptances_user_type;
DROP TABLE IF EXISTS public.legal_acceptances;
COMMIT;
