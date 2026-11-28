-- Terms of Service and Acceptable Use Policy v2.2 (reports and
-- enforcement). Published 30 November 2026: new signups accept terms
-- 2.2; existing accounts are bound from 1 January 2027 after the notice
-- email (Terms §15).
--
-- The 2.0 rows stay active for now, so a console still sending '2.0'
-- during the deploy isn't refused (resolveAcceptedDocuments); a later
-- migration sets their superseded_at. The checksum is the SHA-256 of
-- the source file in this commit.

INSERT INTO public.legal_documents (document_type, version, checksum, effective_at, source_path) VALUES
    ('terms', '2.2', 'eb2c30f93b583828193b1f57e121f3998b4b5a502255062fcac33e9f4864f7fb', '2026-11-30T00:00:00Z', 'docs/legal/v2/terms.md'),
    ('aup',   '2.2', '9f61001aa95efc691035758df54628f69ae3fb833562459852021db2670664c8', '2026-11-30T00:00:00Z', 'docs/legal/v2/aup.md')
ON CONFLICT (document_type, version) DO NOTHING;
