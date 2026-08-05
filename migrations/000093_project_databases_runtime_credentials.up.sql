-- 000093_project_databases_runtime_credentials.up.sql
--
-- Team-tier M2.5 part 2 — foundation for non-owner runtime routing.
--
-- Adds four columns to project_databases so a dedicated instance
-- can carry TWO sets of credentials:
--
--   * the OWNER (existing `username` / `password_*` columns) — the
--     admin login the user sees in the Direct Connection UI (M4),
--     needed by ops for DDL and by the pool-cache when the runtime
--     credentials haven't been populated yet.
--
--   * the RUNTIME (new `runtime_username` / `runtime_password_*`
--     columns) — a non-owner login used by SDK traffic. Non-owner
--     is the whole point of this migration: Postgres skips RLS
--     policies for a table owner (bypassrls semantics), so if the
--     runtime pool connected as the owner, `applyRLSContext`'s
--     app.end_user_* GUCs would be set but never enforced. This
--     would let every end-user of a Team-tier app read every
--     other end-user's rows — worse than Free/Pro. See the loud
--     comment in internal/gateway/router.go where TEAM_TIER_ROUTING
--     is gated, and the safety-checklist on issue #338.
--
-- All four columns are NULLable so existing rows (M1/M2 provisions)
-- remain valid. The pool cache and connection handlers read the
-- runtime credential when present and fall back to the owner
-- credential when it's NULL — that keeps the pre-part-2 behaviour
-- intact until the follow-up worker patch populates them.
--
-- Follow-ups tracked on #338:
--   * ProvisionTeamDatabaseWorker: create the runtime role after
--     bootstrap + provision_tenant, seal its password into these
--     columns.
--   * A regression test asserting end-user A cannot see end-user
--     B's RLS-protected rows on a dedicated instance.

BEGIN;

ALTER TABLE public.project_databases
    ADD COLUMN runtime_username         TEXT     NULL,
    ADD COLUMN runtime_password_ciphertext BYTEA  NULL,
    ADD COLUMN runtime_password_nonce      BYTEA  NULL,
    ADD COLUMN runtime_password_key_version smallint NULL;

-- CHECK: runtime cred fields are all-or-nothing. Either the whole
-- runtime role is populated (username + sealed password + nonce
-- + version) or none of it is. Prevents half-written rows from
-- masquerading as "runtime provisioned" and steering traffic to
-- an unopenable DSN.
ALTER TABLE public.project_databases
    ADD CONSTRAINT project_databases_runtime_all_or_none
    CHECK (
        (runtime_username IS NULL
            AND runtime_password_ciphertext IS NULL
            AND runtime_password_nonce IS NULL
            AND runtime_password_key_version IS NULL)
        OR
        (runtime_username IS NOT NULL
            AND runtime_password_ciphertext IS NOT NULL
            AND runtime_password_nonce IS NOT NULL
            AND runtime_password_key_version IS NOT NULL)
    );

COMMIT;
