-- 000088_staff_secrecy.up.sql
--
-- Team-tier M2b, hard blocker #1 for the German legal-tech beta:
-- staff secrecy register for §203 StGB / §43e BRAO compliance.
--
-- Under §203 (3) StGB (amended 2017 by the
-- "Neuregelung des Schutzes von Berufsgeheimnissen") and §43e BRAO,
-- a German lawyer may use a SaaS only if:
--
--   1. The processor signs a Verschwiegenheitsverpflichtung.
--   2. Every INDIVIDUAL employee with data access is bound to
--      §203-equivalent secrecy in writing — criminally liable, not
--      just contractually.
--
-- This table is the register the customer can pull to attest to their
-- Kammer (bar association) that they've verified their processor's
-- staff are properly bound. Kept in-database — not in a Google Doc
-- or SharePoint — because attorney secrecy compliance can't be
-- delegated to a US-hosted collaboration tool.
--
-- Publicly-readable subset: the anonymous /platform/projects/{id}/
-- compliance/staff-secrecy-register endpoint returns
-- (employee_name, role, declaration_signed_at, revoked_at) only —
-- no PII beyond the name (which is already public via the Impressum
-- + team page).

BEGIN;

CREATE TABLE public.staff_secrecy_declarations (
    id                    UUID        PRIMARY KEY DEFAULT gen_random_uuid(),

    -- Employee identity as it appears on the signed declaration.
    -- Not an FK to any auth table — Eurobase employees may not have
    -- console accounts, and freelancers who sign the declaration
    -- definitely don't.
    employee_name         TEXT        NOT NULL,
    role                  TEXT        NOT NULL,

    -- Wall-clock timestamp of signature on the paper document.
    -- Distinct from created_at (the row-insert time) so backfilling
    -- historical declarations reflects the true signing date.
    declaration_signed_at TIMESTAMPTZ NOT NULL,

    -- SHA-256 of the signed PDF text — lets an auditor verify a
    -- retrieved copy matches what was signed at the time. 64 hex
    -- chars; enforce shape but not content.
    declaration_hash      TEXT        NOT NULL CHECK (declaration_hash ~ '^[0-9a-f]{64}$'),

    -- Non-NULL when the person has left / rescinded the declaration.
    -- Kept in-place rather than deleted so the compliance timeline
    -- stays reconstructible.
    revoked_at            TIMESTAMPTZ,

    created_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Cheap "who's currently bound" query — used by the public register
-- endpoint. Small partial index (bounded by staff count, expected
-- O(dozen) rows).
CREATE INDEX ix_staff_secrecy_current
    ON public.staff_secrecy_declarations(declaration_signed_at DESC)
    WHERE revoked_at IS NULL;

GRANT SELECT ON public.staff_secrecy_declarations TO eurobase_gateway;
GRANT SELECT, INSERT, UPDATE ON public.staff_secrecy_declarations TO eurobase_developer;

COMMIT;
