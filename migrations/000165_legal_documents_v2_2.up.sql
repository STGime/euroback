-- Terms of Service and Acceptable Use Policy v2.2 (reports and
-- enforcement). Published 30 November 2026: new signups accept terms
-- 2.2; existing accounts are bound from 1 January 2027 after the notice
-- email (Terms §15).
--
-- The 2.0 rows stay active for now, so a console still sending '2.0'
-- during the deploy isn't refused (resolveAcceptedDocuments); a later
-- migration sets their superseded_at (planned for 1 December 2026, after
-- the deploy has settled). The checksum is the SHA-256 of
-- the source file in this commit.

INSERT INTO public.legal_documents (document_type, version, checksum, effective_at, source_path) VALUES
    ('terms', '2.2', '69c1af86c7adc5b7763e07ac66ffca2a97dd7b8e158c4bd5d0d432ce70b6eeff', '2026-11-30T00:00:00Z', 'docs/legal/v2/terms.md'),
    ('aup',   '2.2', '78a1ccff5c5cf395333972ff1e247255d5263f7dabae679e2e15279a01d2070a', '2026-11-30T00:00:00Z', 'docs/legal/v2/aup.md')
ON CONFLICT (document_type, version) DO NOTHING;
