-- 000098_audit_export_destinations.up.sql
--
-- Tier-1 GDPR #170 / follow-up #353. Per-tenant SIEM destinations
-- for the audit-log stream. Complements #352's platform-managed
-- object-dump WORM archive with customer-facing push destinations.
--
-- Kinds:
--   * webhook — HMAC-signed POST to a customer HTTPS endpoint
--     (#354). Format json | cef.
--   * syslog  — RFC 5424 over TCP/TLS to a customer syslog sink
--     (#355). Format usually json inside SD-DATA, but format
--     field still applies for a cef-style rendering.
--
-- last_cursor is the audit_log.seq of the most recent successfully-
-- delivered row. At-least-once: on delivery success the deliverer
-- advances last_cursor; on non-success it stays put and the next
-- tick retries. Zero means "never delivered anything yet."
--
-- secret_ref names a key in the tenant vault
-- (public.vault_secrets.name, tenant-scoped by project_id). NULL is
-- legal — a webhook without an HMAC secret is a design choice the
-- tenant might make (public/read-only sinks); the deliverer treats
-- NULL as "sign with empty key" which still lets the sink verify
-- structural fields (timestamp, event count) even if the signature
-- itself is not a secret. The console UI will nudge toward setting
-- one.
--
-- UNIQUE (project_id, endpoint, kind) prevents an operator from
-- registering the same URL twice — accidental duplicate delivery
-- to the same sink is almost never what anyone wants.
--
-- Gated to Legal-Team plan at the handler layer (CheckLegalTeamTier
-- in plans/enforcement.go), matching every other Legal-Team-tier
-- surface in this milestone.

BEGIN;

CREATE TABLE public.audit_export_destinations (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id   UUID        NOT NULL REFERENCES public.projects(id) ON DELETE CASCADE,

    kind         TEXT        NOT NULL CHECK (kind IN ('webhook', 'syslog')),

    -- webhook: "https://example.com/audit"
    -- syslog:  "host:port" (TLS-only; #355 rejects plain-TCP)
    endpoint     TEXT        NOT NULL CHECK (length(endpoint) > 0),

    -- Nullable vault key name. If set, the deliverer resolves it
    -- against public.vault_secrets WHERE project_id = ... AND name = ref
    -- and uses the plaintext for HMAC / TLS client cert. NULL is
    -- legal (see header note).
    secret_ref   TEXT,

    -- json = native envelope {events, cursor}; cef = Common Event
    -- Format for enterprise SIEMs. Enforced by CHECK so an
    -- unimplemented format code can't sneak into the DB via a
    -- misconfigured client.
    format       TEXT        NOT NULL DEFAULT 'json' CHECK (format IN ('json', 'cef')),

    -- Kill-switch. When false, the deliverer skips this row.
    -- Default true so a POSTed row is live immediately (matches
    -- the CRUD contract customers expect from other integrations).
    enabled      BOOLEAN     NOT NULL DEFAULT true,

    -- audit_log.seq of the last successfully-delivered event. 0 =
    -- never delivered yet (the deliverer will start from seq 0 and
    -- fast-forward past pre-registration history rather than
    -- back-fill, so a newly-added destination sees only events from
    -- registration onward — see #354).
    last_cursor  BIGINT      NOT NULL DEFAULT 0,

    created_by   UUID        REFERENCES public.platform_users(id) ON DELETE SET NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (project_id, endpoint, kind)
);

-- Deliverer sweep query: pick enabled destinations, ordered by
-- last_cursor ASC so the most-behind sinks get worked first.
CREATE INDEX ix_audit_export_destinations_enabled
    ON public.audit_export_destinations(project_id, last_cursor ASC)
    WHERE enabled = true;

GRANT SELECT, INSERT, UPDATE, DELETE ON public.audit_export_destinations TO eurobase_gateway;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.audit_export_destinations TO eurobase_developer;

COMMIT;
