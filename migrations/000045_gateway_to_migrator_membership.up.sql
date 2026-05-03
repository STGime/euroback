-- 000045_gateway_to_migrator_membership.up.sql
--
-- Grants eurobase_gateway membership to eurobase_migrator so migrator
-- (and eurobase_developer, which is a member of migrator per 000044)
-- inherits gateway's privileges. Without this, tables created by the
-- gateway role (via the SDK DDL endpoint, or via the platform path
-- before the developer-pool deploy) are invisible to the platform path
-- — every SELECT against them fails with "permission denied".
--
-- This GRANT was originally executed manually after the platform-pool
-- deploy uncovered the gap. Adding it here ensures fresh databases
-- (staging, DR rebuilds, ephemeral CI envs) get the same topology
-- without operator intervention.
--
-- Requires the executing role (eurobase_migrator) to be admin on
-- eurobase_gateway. On Scaleway managed Postgres, eurobase_migrator
-- has the "admin" attribute (CREATEROLE-equivalent), which is
-- sufficient. If this migration is ever run by a less-privileged role
-- it will fail with "permission denied to grant role" — that's the
-- signal to escalate via the Scaleway console (run as eurobase_api or
-- _rdb_superadmin).

BEGIN;

-- Idempotent: re-running is safe; PG silently no-ops a redundant GRANT.
GRANT eurobase_gateway TO eurobase_migrator;

-- Verify the membership took effect — fail fast if it didn't.
DO $$
BEGIN
    IF NOT pg_has_role('eurobase_migrator', 'eurobase_gateway', 'USAGE') THEN
        RAISE EXCEPTION 'GRANT eurobase_gateway TO eurobase_migrator did not take effect (membership graph still missing the gateway leg)';
    END IF;
END$$;

COMMIT;
