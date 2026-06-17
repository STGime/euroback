-- 000065_breach_register.up.sql
--
-- Tier-1 GDPR #4 (breach notification workflow — Art. 33/34). Creates the
-- append-only register of personal-data breaches the DPO is required to
-- maintain under Art. 33(5), including breaches that we judged NOT to be
-- notifiable. Surfaced in the console at Compliance → Breaches and
-- exposed on /platform/projects/{id}/compliance/breaches.
--
-- Append-only by the same pattern as audit_log (#171, migration 000058):
-- the runtime roles get INSERT + SELECT only. UPDATE/DELETE are revoked
-- from eurobase_gateway (the real DB user the breach service connects
-- as). State transitions write a NEW row tying back to incident_id so
-- "edits" remain auditable. The independent off-box WORM dump (#171)
-- catches a compromised gateway forging fresh appends.
--
-- Schema choices:
--   * incident_id groups every row that belongs to one incident
--     (open → updated → notified → closed). This is what gives us
--     append-only semantics for what would otherwise be UPDATE.
--   * `status` mirrors the runbook state machine: open / contained /
--     notified_customers / notified_authority / closed / no_action
--     (the last for the "logged but did not notify" case the runbook
--     calls out — Art. 33(5) requires keeping the record either way).
--   * Categories of data and data subjects are TEXT[] so the report
--     can group by both without forcing a controlled vocabulary up
--     front. The DPO writes them in human-readable form.
--   * mttd_seconds = (awareness_at - occurred_at).
--     mttr_seconds = (resolved_at - awareness_at).
--     Both are stored, not computed, because the closing row may set
--     them while earlier rows leave them NULL. The DPO confirms.
--   * notified_authority_at and notified_customers_at are the
--     timestamps of the Art. 33 and DPA §10 notifications. SLA
--     dashboards subtract these from awareness_at to detect breach of
--     the 72h / 24h commitments.
--   * lead_sa carries the supervisory authority code (e.g. "fr-cnil",
--     "de-bayern"). Default for unbreached incidents stays NULL.
--
-- No explicit BEGIN/COMMIT: golang-migrate wraps each .up.sql in its own tx.

CREATE TABLE public.breach_register (
    id                       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    incident_id              UUID NOT NULL,
    seq                      BIGSERIAL NOT NULL,

    -- Scope
    project_id               UUID REFERENCES public.projects(id) ON DELETE SET NULL,
    affects_platform         BOOLEAN NOT NULL DEFAULT false,

    -- Art. 33(3)/(5) substantive fields
    title                    TEXT NOT NULL,
    description              TEXT NOT NULL DEFAULT '',
    likely_consequences      TEXT NOT NULL DEFAULT '',
    measures_taken           TEXT NOT NULL DEFAULT '',
    data_categories          TEXT[] NOT NULL DEFAULT ARRAY[]::TEXT[],
    subject_categories       TEXT[] NOT NULL DEFAULT ARRAY[]::TEXT[],
    records_affected         BIGINT,
    subjects_affected        BIGINT,

    -- Timeline (Art. 33 SLAs are anchored to awareness_at, not occurred_at)
    occurred_at              TIMESTAMPTZ,
    occurred_until           TIMESTAMPTZ,
    awareness_at             TIMESTAMPTZ NOT NULL,
    contained_at             TIMESTAMPTZ,
    resolved_at              TIMESTAMPTZ,

    -- Notification state
    notified_authority       BOOLEAN NOT NULL DEFAULT false,
    notified_authority_at    TIMESTAMPTZ,
    notified_customers       BOOLEAN NOT NULL DEFAULT false,
    notified_customers_at    TIMESTAMPTZ,
    notified_subjects        BOOLEAN NOT NULL DEFAULT false,
    notified_subjects_at     TIMESTAMPTZ,
    lead_sa                  TEXT,

    -- MTTD/MTTR
    mttd_seconds             BIGINT,
    mttr_seconds             BIGINT,

    -- State machine
    status                   TEXT NOT NULL DEFAULT 'open'
                                 CHECK (status IN ('open', 'contained', 'notified_customers',
                                                   'notified_authority', 'closed', 'no_action')),

    -- Actor + free-form context (JSON for forward compat)
    actor_id                 UUID,
    actor_email              TEXT NOT NULL DEFAULT '',
    note                     TEXT NOT NULL DEFAULT '',
    metadata                 JSONB NOT NULL DEFAULT '{}'::JSONB,

    created_at               TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One row per incident is the "head" (status differentiates open vs closed
-- snapshots); list/UI queries always pull the latest seq per incident.
CREATE INDEX idx_breach_register_incident ON public.breach_register(incident_id, seq DESC);
CREATE INDEX idx_breach_register_project ON public.breach_register(project_id, created_at DESC);
CREATE INDEX idx_breach_register_status ON public.breach_register(status)
    WHERE status IN ('open', 'contained');
CREATE INDEX idx_breach_register_awareness ON public.breach_register(awareness_at DESC);

ALTER TABLE public.breach_register OWNER TO eurobase_migrator;

-- Runtime: gateway INSERTs and SELECTs; UPDATE/DELETE revoked to keep the
-- register tamper-resistant (same pattern as audit_log in 000058). The
-- developer role inherits migrator privileges via SET LOCAL ROLE in the
-- developer path, so platform writes go through migrator regardless;
-- defense-in-depth REVOKE is below.
GRANT SELECT, INSERT ON public.breach_register TO eurobase_gateway;
GRANT USAGE, SELECT ON SEQUENCE public.breach_register_seq_seq TO eurobase_gateway;

REVOKE UPDATE, DELETE ON public.breach_register FROM eurobase_gateway;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'eurobase_developer') THEN
        EXECUTE 'GRANT SELECT, INSERT ON public.breach_register TO eurobase_developer';
        EXECUTE 'GRANT USAGE, SELECT ON SEQUENCE public.breach_register_seq_seq TO eurobase_developer';
        EXECUTE 'REVOKE UPDATE, DELETE ON public.breach_register FROM eurobase_developer';
    END IF;
END$$;
