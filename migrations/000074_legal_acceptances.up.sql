-- 000074_legal_acceptances.up.sql
--
-- Phase A of the public-beta launch plan (docs/public-beta-launch-plan.md).
-- Records per-user acceptance of each versioned legal document. Closes
-- the "Phase 2" gap called out in `docs/legal/v1/dpa.md` and the v1
-- Terms reviewer header — until now signup collected no click-through
-- consent, only implicit "by using the service you accept" language.
-- That is not defensible under GDPR Article 7 for a public-beta launch.
--
-- One row per (user, document_type) per acceptance. Re-accepting a
-- newer version inserts a fresh row rather than updating the old one,
-- so the history is append-only and audit-trail-friendly.

BEGIN;

CREATE TABLE public.legal_acceptances (
    id                UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id           UUID NOT NULL REFERENCES public.platform_users(id) ON DELETE CASCADE,
    document_id       UUID NOT NULL REFERENCES public.legal_documents(id),
    document_type     TEXT NOT NULL,   -- denormalised for read-time queries; matches legal_documents.document_type
    document_version  TEXT NOT NULL,   -- denormalised; matches legal_documents.version
    accepted_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    ip                INET,             -- client IP at click-through; nullable for backfill / test data
    user_agent        TEXT,             -- client UA at click-through
    added_at          TIMESTAMPTZ DEFAULT now()
);

-- Fast lookup: "did user X accept the current version of document Y?"
CREATE INDEX idx_legal_acceptances_user_type
    ON public.legal_acceptances (user_id, document_type, accepted_at DESC);

-- Audit reverse lookup: "who accepted document X?"
CREATE INDEX idx_legal_acceptances_document
    ON public.legal_acceptances (document_id);

COMMIT;
