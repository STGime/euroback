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
PGB_R=eb-pgb-runner
PGB_P=eb-pgb-publisher
PUBVOL=eb-pgb-published
PGB_G=eb-pgb-gateway
PG_PORT="${PGB_TEST_PG_PORT:-5466}"
PGB_PORT="${PGB_TEST_PORT:-6466}"
SECRET="pgbouncer-test-secret-0123456789abcdef"

cleanup() { docker rm -f "$PGB" "$PGB_R" "$PGB_P" "$PGB_G" "$PG" >/dev/null 2>&1 || true; docker volume rm "$PUBVOL" >/dev/null 2>&1 || true; docker network rm "$NET" >/dev/null 2>&1 || true; }
trap cleanup EXIT
[ -n "${PGB_KEEP:-}" ] && trap - EXIT
cleanup
docker network create "$NET" >/dev/null

docker run -d --name "$PG" --network "$NET" -p "$PG_PORT:5432" \
  -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=eurobase \
  -e POSTGRES_HOST_AUTH_METHOD=scram-sha-256 postgres:16-alpine >/dev/null
for _ in $(seq 1 30); do docker exec "$PG" pg_isready -U postgres -d eurobase >/dev/null 2>&1 && break; sleep 1; done
sleep 1
"$REPO_ROOT/scripts/db/apply-migrations.sh" "postgres://postgres:postgres@localhost:$PG_PORT/eurobase?sslmode=disable" | tail -1

echo "── building pgbouncer image"
docker build -q -f "$REPO_ROOT/deploy/docker/Dockerfile.pgbouncer" -t eurobase-pgbouncer:test "$REPO_ROOT" >/dev/null

# Same two steps as the pod (init container, then pgbouncer + sync
# sidecar); here one container so the sidecar can SIGHUP pgbouncer.
docker run -d --name "$PGB" --network "$NET" -p "$PGB_PORT:6432" \
  -e DATABASE_URL="postgres://eurobase_gateway:localdev@$PG:5432/eurobase?sslmode=disable" \
  -e DATABASE_URL_FUNCTION_RUNNER="postgres://eurobase_function_runner:localdev@$PG:5432/eurobase?sslmode=disable" \
  -e FUNC_PASSWORD_SECRET="$SECRET" \
  -e PGB_SERVER_TLS=disable -e PGB_SYNC_INTERVAL=2s -e PGB_POOL_SIZE_EUROBASE_GATEWAY=1 \
  -e PGB_TENANT_POOL_SIZE=1 -e PGB_QUERY_WAIT_TIMEOUT=2 \
  eurobase-pgbouncer:test \
  sh -c 'pgbouncer-userlist init && { pgbouncer-userlist sync & } && exec pgbouncer /run/pgbouncer/pgbouncer.ini' >/dev/null
# The two production modes (#651): runner pooler (tenants only, lists
# tenants as the runner role) and gateway pooler (platform roles only).
# #653: the publisher (the worker's role in production) derives the tenant
# verifiers and writes them to a shared volume; the runner pooler only
# reads them — it gets no FUNC_PASSWORD_SECRET and no database URL.
docker volume create "$PUBVOL" >/dev/null
docker run -d --name "$PGB_P" --network "$NET" -v "$PUBVOL:/pub" --user 0 \
  -e DATABASE_URL="postgres://eurobase_gateway:localdev@$PG:5432/eurobase?sslmode=disable" \
  -e FUNC_PASSWORD_SECRET="$SECRET" -e PGB_PUBLISH_TO=file:/pub -e PGB_SYNC_INTERVAL=2s \
  eurobase-pgbouncer:test pgbouncer-userlist publish >/dev/null
docker run -d --name "$PGB_R" --network "$NET" -p 6467:6432 -p 9128:9127 -v "$PUBVOL:/pub:ro" \
  -e PGB_TENANT_SOURCE=file:/pub -e PGB_PLATFORM_URL_VARS= \
  -e PGB_SERVER_TLS=disable -e PGB_SYNC_INTERVAL=2s \
  eurobase-pgbouncer:test \
  sh -c 'pgbouncer-userlist init && { pgbouncer-userlist sync & } && exec pgbouncer /run/pgbouncer/pgbouncer.ini' >/dev/null
docker run -d --name "$PGB_G" --network "$NET" -p 6468:6432 -p 9129:9127 \
  -e DATABASE_URL="postgres://eurobase_gateway:localdev@$PG:5432/eurobase?sslmode=disable" \
  -e PGB_PLATFORM_URL_VARS=DATABASE_URL -e PGB_INCLUDE_TENANTS=0 \
  -e PGB_SERVER_TLS=disable -e PGB_SYNC_INTERVAL=2s \
  eurobase-pgbouncer:test \
  sh -c 'pgbouncer-userlist init && { pgbouncer-userlist sync & } && exec pgbouncer /run/pgbouncer/pgbouncer.ini' >/dev/null
sleep 3
# The runner pooler holds no secret (#653).
docker inspect "$PGB_R" --format '{{range .Config.Env}}{{println .}}{{end}}' | grep -E "FUNC_PASSWORD_SECRET|DATABASE_URL" \
  && { echo "runner pooler has a secret in its environment"; exit 1; } || true
for c in "$PGB" "$PGB_R" "$PGB_P" "$PGB_G"; do
  docker logs "$c" 2>&1 | grep -E '"level":"ERROR"|\] (FATAL|ERROR|WARNING) ' && { echo "$c reported errors"; exit 1; } || true
done

cd "$REPO_ROOT"
PGB_TEST_POOLED_GATEWAY="postgres://eurobase_gateway:localdev@localhost:$PGB_PORT/eurobase?sslmode=disable" \
PGB_TEST_POOLED_BASE="postgres://x:x@localhost:$PGB_PORT/eurobase_tenant?sslmode=disable" \
PGB_TEST_DEV_URL="postgres://eurobase_developer:localdev@localhost:$PG_PORT/eurobase?sslmode=disable" \
PGB_TEST_SECRET="$SECRET" \
  go test ./internal/pgbouncerconf/ -run TestPgBouncerEndToEnd -count=1 -v

# After the e2e test provisioned a tenant: the prod-mode split (#651).
sleep 6 # publisher publishes the new tenant, runner pooler's sidecar picks it up
PGB_TEST_RUNNER_POOLER="postgres://localhost:6467" PGB_TEST_GATEWAY_POOLER="postgres://localhost:6468" \
PGB_TEST_RUNNER_METRICS="http://localhost:9128/metrics" PGB_TEST_GATEWAY_METRICS="http://localhost:9129/metrics" \
PGB_TEST_SECRET="$SECRET" \
  go test ./internal/pgbouncerconf/ -run TestPoolerSplit -count=1 -v

# The runner's driver (postgres.js) through the pooler, as the runner uses it.
if command -v deno >/dev/null; then
  echo "── postgres.js probe"
  PGB_PROBE_GATEWAY="postgres://eurobase_gateway:localdev@localhost:$PGB_PORT/eurobase?sslmode=disable" \
  PGB_PROBE_TENANT_BASE="postgres://x:x@localhost:$PGB_PORT/eurobase_tenant?sslmode=disable" \
  PGB_TEST_SECRET="$SECRET" \
    deno run --allow-net --allow-env --allow-read --no-lock scripts/pgbouncer-probe.ts
fi
