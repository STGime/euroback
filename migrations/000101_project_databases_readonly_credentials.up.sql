-- 000101_project_databases_readonly_credentials.up.sql
--
-- Team-tier: add a THIRD credential slot on project_databases for the
-- non-owner, SELECT-only `eurobase_readonly` role that the dedicated
-- bootstrap now provisions (see internal/dbprovider/dedicated_bootstrap.sql
-- and bootstrap.go — same shape as the runtime cred added in 000093).
--
-- Why a distinct role:
--   * The M4 Direct Connection UI offers "Read-only" as one of two
--     modes for handing out a DSN to analysts / read-replica configs
--     / dashboards. Before this migration the "read-only" toggle
--     silently fell back to the OWNER credential with a
--     `readonly_pending: true` banner (see connection_handlers.go
--     M4 TODO). A user who copies without reading the banner hands
--     out full read/write access — footgun.
--   * The runtime credential (000093) is read/write on the tenant
--     schema (SDK CRUD + auth flows need INSERT/UPDATE/DELETE), so
--     it can't double as the read-only DSN either.
--
-- Same all-or-nothing CHECK pattern as 000093 keeps half-written rows
-- from masquerading as "readonly provisioned".
--
-- Backwards-compatible: NULL means the readonly role hasn't been
-- provisioned yet on this instance (pre-bootstrap-upgrade rows).
-- The connection handler falls back to the pre-000101 behaviour
-- (owner cred + `readonly_pending: true`) for those. Future bootstrap
-- runs populate the columns.

BEGIN;

ALTER TABLE public.project_databases
    ADD COLUMN readonly_username             TEXT     NULL,
    ADD COLUMN readonly_password_ciphertext  BYTEA    NULL,
    ADD COLUMN readonly_password_nonce       BYTEA    NULL,
    ADD COLUMN readonly_password_key_version smallint NULL;

ALTER TABLE public.project_databases
    ADD CONSTRAINT project_databases_readonly_all_or_none
    CHECK (
        (readonly_username IS NULL
            AND readonly_password_ciphertext IS NULL
            AND readonly_password_nonce IS NULL
            AND readonly_password_key_version IS NULL)
        OR
        (readonly_username IS NOT NULL
            AND readonly_password_ciphertext IS NOT NULL
            AND readonly_password_nonce IS NOT NULL
            AND readonly_password_key_version IS NOT NULL)
    );

COMMIT;
