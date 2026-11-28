-- Fails while acceptances reference the 2.2 rows (legal_acceptances FK);
-- those records must be kept.
DELETE FROM public.legal_documents
 WHERE (document_type, version) IN (('terms', '2.2'), ('aup', '2.2'));
