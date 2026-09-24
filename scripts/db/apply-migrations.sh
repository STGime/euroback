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
#   3. Every later migration through the REAL prod migrations image
#      (deploy/docker/Dockerfile.migrations, built from this tree) with the
#      prod Job's args, AS eurobase_migrator — so migrate-entrypoint.sh's
#      own GRANT + REASSIGN runs exactly as it does before every prod `up`.
#
# Re-running on an up-to-date database is a no-op; on a database already
# past 37 only step 3 runs (incremental local updates).
#
# Local / CI reference only — NOT a Scaleway DR tool: it creates roles
# (incl. a SUPERUSER stand-in), uses one password for all of them and
# connects with sslmode=disable. For a real environment follow the console
# bootstrap in CLAUDE.md § Postgres roles.
#
# Env: ROLE_PASSWORD (default "localdev") — password for all runtime
# roles; MIGRATE_IMAGE — golang-migrate CLI image (default: the FROM line
# of deploy/docker/Dockerfile.migrations).
set -euo pipefail

ADMIN_URL="${1:?usage: apply-migrations.sh <superuser-url>}"
REPO_ROOT="$(git -C "$(dirname "$0")" rev-parse --show-toplevel)"
# Same golang-migrate image as the prod migrate Job.
MIGRATE_IMAGE="${MIGRATE_IMAGE:-$(sed -nE 's/^FROM[[:space:]]+([^[:space:]]+).*/\1/p' "$REPO_ROOT/deploy/docker/Dockerfile.migrations" | head -1)}"
ROLE_PASSWORD="${ROLE_PASSWORD:-localdev}"
LAST_PRE_SPLIT=37 # 000037_role_split (run by the admin role) hands ownership to eurobase_migrator

# Host + db from the URL; the golang-migrate container needs to reach it.
URL_NOQUERY="${ADMIN_URL%%\?*}"
case "$URL_NOQUERY" in
    postgres://*@*/*|postgresql://*@*/*) ;;
    *) echo "expected postgres://user:pass@host:port/db, got: ${URL_NOQUERY%%@*}@…" >&2; exit 2 ;;
esac
HOSTPORT="$(echo "$URL_NOQUERY" | sed -E 's#^postgres(ql)?://[^@]+@([^/]+)/.*#\2#')"
DBNAME="${URL_NOQUERY##*/}"
case "$ROLE_PASSWORD" in
    *[@/?#%:]*) echo "ROLE_PASSWORD must not contain @ / ? # % : (it's embedded in a URL)" >&2; exit 2 ;;
esac
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
\o /dev/null
SELECT set_config('replay.pw', :'pw', false);
\o
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
-- (the DB and schema public belong to _rdb_superadmin / pg_database_owner).
-- Only CONNECT and USAGE carry GRANT OPTION — the two documented Scaleway-
-- support grants (CLAUDE.md § Postgres roles). CREATE is granted WITHOUT
-- it, so a migration that tries to re-grant CREATE fails here too instead
-- of passing CI and becoming a silent no-op in prod.
DO $$
BEGIN
    EXECUTE format('GRANT CONNECT ON DATABASE %I TO eurobase_migrator WITH GRANT OPTION', current_database());
    EXECUTE format('GRANT CREATE ON DATABASE %I TO eurobase_migrator', current_database());
END$$;
GRANT USAGE  ON SCHEMA public TO eurobase_migrator WITH GRANT OPTION;
GRANT CREATE ON SCHEMA public TO eurobase_migrator;
-- Memberships the migrator can't grant itself on PG16 (no self-grant, no
-- ADMIN on console-created roles). Options spelled out: managed-PG defaults
-- unspecified options to FALSE. 000044 / 000045 verify them.
GRANT eurobase_migrator TO eurobase_developer WITH INHERIT TRUE, SET TRUE;
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

# Build the prod migrations image from this tree. Minimal context (only what
# the Dockerfile COPYs) so local node_modules etc. aren't sent to Docker.
PROD_IMAGE=eurobase-migrations:replay
echo "── Building $PROD_IMAGE (deploy/docker/Dockerfile.migrations) ──"
(cd "$REPO_ROOT" && tar -cf - migrations deploy/docker/Dockerfile.migrations deploy/docker/migrate-entrypoint.sh) \
    | docker build -q -f deploy/docker/Dockerfile.migrations -t "$PROD_IMAGE" - >/dev/null || fail

echo "── Remaining migrations via the prod image/entrypoint as eurobase_migrator ──"
MIGRATOR_URL="$(role_url eurobase_migrator)"
docker run --rm "${DOCKER_NET[@]}" -e DATABASE_URL_MIGRATOR="$MIGRATOR_URL" \
    "$PROD_IMAGE" -database "$MIGRATOR_URL" up || fail

LATEST="$(ls "$REPO_ROOT"/migrations/*.up.sql | sed -E 's#.*/0*([0-9]+)_.*#\1#' | sort -n | tail -1)"
APPLIED="$(sql -c "SELECT version FROM schema_migrations WHERE NOT dirty")"
if [ "$APPLIED" != "$LATEST" ]; then
    echo "FAILED: schema_migrations at ${APPLIED:-?}, expected $LATEST" >&2
    exit 1
fi
echo "OK: migrations applied through $LATEST."
