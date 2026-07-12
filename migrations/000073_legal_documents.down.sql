BEGIN;
DROP INDEX IF EXISTS public.idx_legal_documents_current;
DROP TABLE IF EXISTS public.legal_documents;
COMMIT;
