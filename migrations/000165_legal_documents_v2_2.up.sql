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
    ('terms', '2.2', '53c27567a42da75fa1977e06acef0b6a6895c39fee4da99f909a6c065a03bc62', '2026-11-30T00:00:00Z', 'docs/legal/v2/terms.md'),
    ('aup',   '2.2', '36fcb9a77e6f7b79df84e04556710bf72df6ce2e58d3cbe24ecb6e9ed6a1980e', '2026-11-30T00:00:00Z', 'docs/legal/v2/aup.md')
ON CONFLICT (document_type, version) DO NOTHING;
