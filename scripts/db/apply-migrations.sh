#!/usr/bin/env bash
# Apply migrations/ to a PostgreSQL database the way production does (#632).
#
#   scripts/db/apply-migrations.sh <superuser-url>
#
# <superuser-url> must be a superuser that is NOT eurobase_api (the
# built-in "postgres" user of the container is fine): migration 000037
# REASSIGNs everything owned by eurobase_api, which PostgreSQL refuses for
# the bootstrap superuser.
#
# Steps, mirroring production history:
#   1. Console bootstrap (idempotent): the runtime login roles, the
#      grants eurobase_migrator holds on Scaleway-owned objects, and the
#      two role memberships the migrator cannot grant itself on PG16.
#      See CLAUDE.md § Postgres roles.
#   2. Migrations 1–37 as eurobase_api (the pre-split admin role;
#      SUPERUSER here stands in for the Scaleway admin user). 000037 hands
#      ownership to eurobase_migrator.
#   3. REASSIGN OWNED BY eurobase_api (what deploy/docker/migrate-entrypoint.sh
#      does before every prod run), then every later migration AS
#      eurobase_migrator — the prod migrate Job's role.
#
# Re-running on an up-to-date database is a no-op; on a database already
# past 37 only step 3 runs (incremental local updates).
#
# Env: ROLE_PASSWORD (default "localdev") — password for all runtime
# roles; MIGRATE_IMAGE — golang-migrate CLI image (default matches
# deploy/docker/Dockerfile.migrations).
set -euo pipefail

ADMIN_URL="${1:?usage: apply-migrations.sh <superuser-url>}"
REPO_ROOT="$(git -C "$(dirname "$0")" rev-parse --show-toplevel)"
MIGRATE_IMAGE="${MIGRATE_IMAGE:-migrate/migrate:v4.18.3}"
ROLE_PASSWORD="${ROLE_PASSWORD:-localdev}"
LAST_PRE_SPLIT=37 # 000037_role_split (run by the admin role) hands ownership to eurobase_migrator

# Host + db from the URL; the golang-migrate container needs to reach it.
HOSTPORT="$(echo "$ADMIN_URL" | sed -E 's#^postgres(ql)?://[^@]+@([^/]+)/.*#\2#')"
DBNAME="$(echo "$ADMIN_URL" | sed -E 's#^.*/([^/?]+)(\?.*)?$#\1#')"
if [ "$(uname)" = "Darwin" ]; then
    # Docker Desktop: reach the host's published ports via host.docker.internal.
    HOSTPORT="$(echo "$HOSTPORT" | sed -E 's#^(localhost|127\.0\.0\.1)#host.docker.internal#')"
    DOCKER_NET=(--add-host=host.docker.internal:host-gateway)
else
    DOCKER_NET=(--network host)
fi
role_url() { echo "postgres://$1:$ROLE_PASSWORD@$HOSTPORT/$DBNAME?sslmode=disable"; }

sql() { psql "$ADMIN_URL" -v ON_ERROR_STOP=1 -qtA "$@"; }

migrate_as() { # <role> <migrate args…>
    local role="$1"; shift
    docker run --rm "${DOCKER_NET[@]}" \
        -v "$REPO_ROOT/migrations:/migrations:ro" \
        "$MIGRATE_IMAGE" -path=/migrations -database "$(role_url "$role")" "$@"
}

fail() {
    echo >&2
    echo "FAILED. schema_migrations: $(sql -c 'SELECT version, dirty FROM schema_migrations' 2>/dev/null || echo '(none)')" >&2
    exit 1
}

# A database created by the old setup-local.sh (psql loop, errors ignored)
# has tables but no schema_migrations — golang-migrate would start at 1
# and trip over them. There's no reliable way to tell what was applied.
if [ "$(sql -c "SELECT to_regclass('public.platform_users') IS NOT NULL AND to_regclass('public.schema_migrations') IS NULL")" = "t" ]; then
    echo "This database was built without golang-migrate (no schema_migrations) and can't be upgraded in place." >&2
    echo "Recreate it — locally: docker compose down -v && scripts/setup-local.sh (deletes local data)." >&2
    exit 1
fi

echo "── Console bootstrap (roles + grants, idempotent) ──"
sql -v pw="$ROLE_PASSWORD" <<'SQL'
SELECT set_config('replay.pw', :'pw', false);
DO $$
DECLARE pw text := current_setting('replay.pw');
BEGIN
    -- Console-created login roles (must exist before 000037/44/47).
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'eurobase_api') THEN
        EXECUTE format('CREATE ROLE eurobase_api LOGIN SUPERUSER PASSWORD %L', pw);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'eurobase_migrator') THEN
        EXECUTE format('CREATE ROLE eurobase_migrator LOGIN CREATEROLE NOINHERIT PASSWORD %L', pw);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'eurobase_gateway') THEN
        EXECUTE format('CREATE ROLE eurobase_gateway LOGIN PASSWORD %L', pw);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'eurobase_developer') THEN
        EXECUTE format('CREATE ROLE eurobase_developer LOGIN INHERIT PASSWORD %L', pw);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'eurobase_function_runner') THEN
        EXECUTE format('CREATE ROLE eurobase_function_runner LOGIN PASSWORD %L', pw);
    END IF;
END$$;
-- Grants eurobase_migrator holds on Scaleway on objects it does NOT own
-- (the DB and schema public belong to _rdb_superadmin / pg_database_owner),
-- obtained once from Scaleway support.
DO $$
BEGIN
    EXECUTE format('GRANT CONNECT, CREATE ON DATABASE %I TO eurobase_migrator WITH GRANT OPTION', current_database());
END$$;
GRANT USAGE, CREATE ON SCHEMA public TO eurobase_migrator WITH GRANT OPTION;
-- Memberships the migrator can't grant itself on PG16 (no self-grant, no
-- ADMIN on console-created roles). 000044 / 000045 verify them.
GRANT eurobase_migrator TO eurobase_developer WITH INHERIT TRUE;
GRANT eurobase_gateway  TO eurobase_migrator  WITH INHERIT TRUE;
SQL

CURRENT=0
if [ "$(sql -c "SELECT to_regclass('public.schema_migrations') IS NOT NULL")" = "t" ]; then
    CURRENT="$(sql -c "SELECT COALESCE(max(version), 0) FROM schema_migrations")"
fi
if [ "${CURRENT:-0}" -lt "$LAST_PRE_SPLIT" ]; then
    echo "── Migrations up to $LAST_PRE_SPLIT as eurobase_api ($MIGRATE_IMAGE) ──"
    migrate_as eurobase_api goto "$LAST_PRE_SPLIT" || fail
fi

echo "── REASSIGN OWNED BY eurobase_api TO eurobase_migrator ──"
sql -c "REASSIGN OWNED BY eurobase_api TO eurobase_migrator" || fail

echo "── Remaining migrations as eurobase_migrator ──"
migrate_as eurobase_migrator up || fail

LATEST="$(ls "$REPO_ROOT"/migrations/*.up.sql | sed -E 's#.*/0*([0-9]+)_.*#\1#' | sort -n | tail -1)"
APPLIED="$(sql -c "SELECT version FROM schema_migrations WHERE NOT dirty")"
if [ "$APPLIED" != "$LATEST" ]; then
    echo "FAILED: schema_migrations at ${APPLIED:-?}, expected $LATEST" >&2
    exit 1
fi
echo "OK: migrations applied through $LATEST."
