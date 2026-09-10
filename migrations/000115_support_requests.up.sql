-- 000115_support_requests.up.sql
--
-- Team-tier "Priority email support" ticket store. Rows are inserted by
-- the authenticated `POST /platform/support/request` handler; a
-- superadmin later triages via `/admin/support-requests` (UI lands in a
-- follow-up). Discord notification fires on insert so ops sees new
-- tickets in real time without polling the admin panel.
--
-- Design notes:
--
--   * `platform_user_id` is nullable so an account deletion doesn't
--     cascade-nuke historic tickets. `ON DELETE SET NULL` + a
--     denormalised `email` column keeps the ticket auditable even if
--     the caller's account goes away mid-flight. Same reason the
--     admin `resolved_by` FK is nullable + SET NULL.
--
--   * Category + priority use CHECK constraints (not a lookup table).
--     The set is small and stable; a follow-up that adds 'partner' or
--     'incident' can extend the CHECK in a one-line migration.
--
--   * Optional `org_id` and `project_id` FKs — the form pre-fills them
--     when the user was viewing an org or project when they filed. Both
--     are `ON DELETE SET NULL` so a project/org delete doesn't lose the
--     ticket.
--
--   * The whole surface is authenticated + Team-tier gated. Runs under
--     the `eurobase_developer` pool, not the SDK-facing gateway pool —
--     so we `REVOKE ALL FROM eurobase_gateway` (same #443-class
--     safeguard as `contact_requests` / `organizations`) and grant the
--     developer role the read-write surface it needs.
--
-- No explicit BEGIN/COMMIT: golang-migrate wraps each .up.sql in its own tx.

CREATE TABLE public.support_requests (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Nullable + SET NULL so account deletion preserves historic
    -- tickets. Email is denormalised at insert time to survive.
    platform_user_id  UUID NULL REFERENCES public.platform_users(id) ON DELETE SET NULL,
    email             TEXT NOT NULL,
    subject           TEXT NOT NULL,
    message           TEXT NOT NULL,
    category          TEXT NOT NULL DEFAULT 'question',
    priority          TEXT NOT NULL DEFAULT 'normal',
    -- Optional context. Form pre-fills these when the user filed
    -- from an org or project page. Both ON DELETE SET NULL — an
    -- unrelated project delete shouldn't erase a support ticket.
    org_id            UUID NULL REFERENCES public.organizations(id) ON DELETE SET NULL,
    project_id        UUID NULL REFERENCES public.projects(id)      ON DELETE SET NULL,
    -- Spam-correlation fingerprints. Same shape as contact_requests.
    user_agent        TEXT NULL,
    ip_address        INET NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Resolution bookkeeping.
    resolved_at       TIMESTAMPTZ NULL,
    resolved_by       UUID NULL REFERENCES public.platform_users(id) ON DELETE SET NULL,
    resolution_note   TEXT NULL,

    -- Bounds match the handler's validation layer. Belt-and-braces so a
    -- bypassed validator (or a direct SQL insert by a compromised
    -- developer role) still can't wedge the table with 10 MB messages.
    CHECK (char_length(email)   BETWEEN 3 AND 254),
    CHECK (char_length(subject) BETWEEN 1 AND 200),
    -- Room for longer bodies than contact_requests (5,000) because
    -- Team callers routinely paste stack traces + queries.
    CHECK (char_length(message) BETWEEN 1 AND 10000),
    CHECK (category IN ('question', 'bug', 'billing', 'feature', 'other')),
    CHECK (priority IN ('normal', 'urgent'))
);

-- Admin-triage index — unresolved tickets sorted newest-first. Partial
-- so it stays tiny once ops catches up. Mirrors the contact_requests
-- pattern.
CREATE INDEX ix_support_requests_unresolved_recent
    ON public.support_requests (created_at DESC)
    WHERE resolved_at IS NULL;

-- Per-user history — for future "your open tickets" list. Cheap
-- secondary index.
CREATE INDEX ix_support_requests_by_user
    ON public.support_requests (platform_user_id, created_at DESC);

-- ── Grants ─────────────────────────────────────────────────────────
-- Same #443-class pitfall as contact_requests / organizations:
-- migration 000037's ALTER DEFAULT PRIVILEGES auto-grants the gateway
-- pool full DML on every new migrator-owned public.* table. Support
-- tickets carry PII (message body, ip, email); the gateway pool is
-- SDK-facing and shouldn't be able to SELECT them. REVOKE first,
-- GRANT explicitly to the developer pool only.
REVOKE ALL ON public.support_requests FROM eurobase_gateway;
GRANT SELECT, INSERT, UPDATE ON public.support_requests TO eurobase_developer;

COMMENT ON TABLE public.support_requests IS
  'Team-tier priority-support tickets (24h SLA). Insert via authenticated POST /platform/support/request. Console triage via /admin/support-requests (follow-up).';
COMMENT ON COLUMN public.support_requests.platform_user_id IS
  'Caller''s user id. NULL after account deletion (SET NULL); email column preserves the address for the historic record.';
COMMENT ON COLUMN public.support_requests.priority IS
  '''normal'' (24h SLA) or ''urgent'' (marker only — actual SLA policy lives outside the DB).';
COMMENT ON COLUMN public.support_requests.org_id IS
  'Optional org context — form pre-fills this if the user was viewing an org page.';
