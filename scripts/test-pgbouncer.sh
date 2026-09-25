#!/usr/bin/env bash
# End-to-end test of the PgBouncer setup (#641): builds the pgbouncer
# image, starts a migrated PG16 + PgBouncer (init + sync, like the pod)
# on a throwaway docker network, and runs
# internal/pgbouncerconf TestPgBouncerEndToEnd against it.
#
# Usage: scripts/test-pgbouncer.sh   (PGB_KEEP=1 keeps the containers for debugging)
set -euo pipefail
REPO_ROOT="$(git -C "$(dirname "$0")" rev-parse --show-toplevel)"
NET=eb-pgb-test
PG=eb-pgb-pg
PGB=eb-pgb-bouncer
PG_PORT="${PGB_TEST_PG_PORT:-5466}"
PGB_PORT="${PGB_TEST_PORT:-6466}"
SECRET="pgbouncer-test-secret-0123456789abcdef"

cleanup() { docker rm -f "$PGB" "$PG" >/dev/null 2>&1 || true; docker network rm "$NET" >/dev/null 2>&1 || true; }
trap cleanup EXIT
[ -n "${PGB_KEEP:-}" ] && trap - EXIT
cleanup
docker network create "$NET" >/dev/null

docker run -d --name "$PG" --network "$NET" -p "$PG_PORT:5432" \
  -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=eurobase postgres:16-alpine >/dev/null
for _ in $(seq 1 30); do docker exec "$PG" pg_isready -U postgres -d eurobase >/dev/null 2>&1 && break; sleep 1; done
sleep 1
"$REPO_ROOT/scripts/db/apply-migrations.sh" "postgres://postgres:postgres@localhost:$PG_PORT/eurobase?sslmode=disable" | tail -1

echo "── building pgbouncer image"
docker build -q -f "$REPO_ROOT/deploy/docker/Dockerfile.pgbouncer" -t eurobase-pgbouncer:test "$REPO_ROOT" >/dev/null

# Same two steps as the pod (init container, then pgbouncer + sync
# sidecar); here one container so the sidecar can SIGHUP pgbouncer.
docker run -d --name "$PGB" --network "$NET" -p "$PGB_PORT:6432" \
  -e DATABASE_URL="postgres://eurobase_gateway:localdev@$PG:5432/eurobase?sslmode=disable" \
  -e DATABASE_URL_DEVELOPER="postgres://eurobase_developer:localdev@$PG:5432/eurobase?sslmode=disable" \
  -e DATABASE_URL_FUNCTION_RUNNER="postgres://eurobase_function_runner:localdev@$PG:5432/eurobase?sslmode=disable" \
  -e FUNC_PASSWORD_SECRET="$SECRET" \
  -e PGB_SERVER_TLS=disable -e PGB_SYNC_INTERVAL=2s -e PGB_POOL_SIZE_EUROBASE_GATEWAY=1 \
  eurobase-pgbouncer:test \
  sh -c 'pgbouncer-userlist init && { pgbouncer-userlist sync & } && exec pgbouncer /run/pgbouncer/pgbouncer.ini' >/dev/null
sleep 3
docker logs "$PGB" 2>&1 | grep -iE "error|fatal|warning" && { echo "pgbouncer reported errors"; exit 1; } || true

cd "$REPO_ROOT"
PGB_TEST_POOLED_GATEWAY="postgres://eurobase_gateway:localdev@localhost:$PGB_PORT/eurobase?sslmode=disable" \
PGB_TEST_POOLED_BASE="postgres://x:x@localhost:$PGB_PORT/eurobase?sslmode=disable" \
PGB_TEST_DEV_URL="postgres://eurobase_developer:localdev@localhost:$PG_PORT/eurobase?sslmode=disable" \
PGB_TEST_SECRET="$SECRET" \
  go test ./internal/pgbouncerconf/ -run TestPgBouncerEndToEnd -count=1 -v
