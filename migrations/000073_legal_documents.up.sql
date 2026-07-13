-- 000073_legal_documents.up.sql
--
-- Phase A of the public-beta launch plan (docs/public-beta-launch-plan.md).
-- Introduces the `legal_documents` registry — a versioned list of the
-- Terms / Privacy / DPA / AUP / Cookies / Sub-processors documents the
-- platform requires users to accept.
--
-- Each row is keyed on (document_type, version) and stores the SHA-256
-- checksum of the served document body, so the console + signup handler
-- can:
--
--   (a) tell users which version they're accepting at click-through,
--   (b) detect drift between the DB-recorded version and the file the
--       gateway is actually serving (if checksums diverge, the deploy
--       is broken and signups should refuse).
--
-- The checksum values below are placeholders — the CI publish step
-- computes SHA-256 over `docs/legal/v2/*.md` after any post-formation
-- placeholder substitution and updates these rows via a follow-up
-- migration. Publishing with the placeholder checksums is fine as long
-- as the corresponding `.md` files still contain `{{LEGAL_ENTITY}}`
-- etc. (which they do until the entity strings land).
--
-- Companion table `legal_acceptances` (migration 000074) records per-
-- user click-through against these rows.

BEGIN;

CREATE TABLE public.legal_documents (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    document_type   TEXT NOT NULL,                       -- 'terms' | 'privacy' | 'dpa' | 'aup' | 'cookies' | 'sub_processors'
    version         TEXT NOT NULL,                       -- '2.0'
    checksum        TEXT NOT NULL,                       -- sha256 hex of the served body; 'PENDING' until CI computes
    effective_at    TIMESTAMPTZ NOT NULL DEFAULT now(),  -- when this version becomes the required one
    superseded_at   TIMESTAMPTZ,                         -- filled when a newer version supersedes this one
    source_path     TEXT NOT NULL,                       -- 'docs/legal/v2/terms.md'
    active          BOOLEAN NOT NULL DEFAULT true,       -- false = withdrawn (e.g. published with a bug, do NOT accept)
    added_at        TIMESTAMPTZ DEFAULT now(),
    UNIQUE (document_type, version)
);

-- Fast lookup for "the current required version of document X".
CREATE INDEX idx_legal_documents_current
    ON public.legal_documents (document_type, effective_at DESC)
    WHERE active = true AND superseded_at IS NULL;

-- Seed the v2 set — the versions accepted at signup during public beta.
-- Checksums land as 'PENDING' — the deploy pipeline UPDATEs them once
-- the placeholder substitution is done and the served content is stable.
INSERT INTO public.legal_documents (document_type, version, checksum, source_path) VALUES
    ('terms',           '2.0', 'PENDING', 'docs/legal/v2/terms.md'),
    ('privacy',         '2.0', 'PENDING', 'docs/legal/v2/privacy.md'),
    ('dpa',             '2.0', 'PENDING', 'docs/legal/v2/dpa.md'),
    ('aup',             '2.0', 'PENDING', 'docs/legal/v2/aup.md'),
    ('cookies',         '2.0', 'PENDING', 'docs/legal/v2/cookies.md'),
    ('sub_processors',  '2.0', 'PENDING', 'docs/legal/v2/sub-processors.md');

COMMIT;
