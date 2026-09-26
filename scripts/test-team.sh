#!/usr/bin/env bash
# Team-tier end-to-end harness (#683): a shared PG16 (all migrations, prod
# roles) plus a separate "dedicated" PG16 standing in for a Team
# project's Scaleway instance (non-superuser admin `eurobase_owner`, as on
# Scaleway), then internal/gateway TestTeamEndToEnd drives the real
# gateway router against a Team project routed to it.
#
# Usage: scripts/test-team.sh   (TEAM_KEEP=1 keeps the containers)
set -euo pipefail
REPO_ROOT="$(git -C "$(dirname "$0")" rev-parse --show-toplevel)"
SH=eb-team-shared
DED=eb-team-dedicated
SH_PORT="${TEAM_SHARED_PORT:-5470}"
DED_PORT="${TEAM_DED_PORT:-5471}"

cleanup() { docker rm -f "$SH" "$DED" >/dev/null 2>&1 || true; [ -z "${CERTS:-}" ] || rm -rf "$CERTS"; }
[ -n "${TEAM_KEEP:-}" ] || trap cleanup EXIT
cleanup

docker run -d --name "$SH" -p "$SH_PORT:5432" -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=eurobase \
  -e POSTGRES_HOST_AUTH_METHOD=scram-sha-256 postgres:16-alpine >/dev/null
# The dedicated pool dials with sslmode=require (as against Scaleway), so
# this one serves TLS with a throwaway self-signed cert.
CERTS="$(mktemp -d)"
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj /CN=localhost \
  -keyout "$CERTS/server.key" -out "$CERTS/server.crt" >/dev/null 2>&1
chmod 644 "$CERTS/server.key"
docker run -d --name "$DED" -p "$DED_PORT:5432" -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=postgres \
  -e POSTGRES_HOST_AUTH_METHOD=scram-sha-256 -v "$CERTS:/certs:ro" --entrypoint sh postgres:16-alpine -c '
    install -o postgres -g postgres -m 600 /certs/server.key /tmp/server.key
    install -o postgres -g postgres -m 644 /certs/server.crt /tmp/server.crt
    exec docker-entrypoint.sh postgres -c ssl=on -c ssl_cert_file=/tmp/server.crt -c ssl_key_file=/tmp/server.key' >/dev/null
for c in "$SH" "$DED"; do
  for _ in $(seq 1 30); do docker exec "$c" pg_isready -U postgres >/dev/null 2>&1 && break; sleep 1; done
done
sleep 1

"$REPO_ROOT/scripts/db/apply-migrations.sh" "postgres://postgres:postgres@localhost:$SH_PORT/eurobase?sslmode=disable" | tail -1

# The "dedicated" instance: like Scaleway's managed admin, eurobase_owner is
# NOT a superuser (CREATEROLE + CREATEDB) and owns the `eurobase` database.
docker exec -i "$DED" psql -U postgres -q -v ON_ERROR_STOP=1 <<'SQL'
CREATE ROLE eurobase_owner LOGIN PASSWORD 'ownerpw' CREATEROLE CREATEDB;
CREATE DATABASE eurobase OWNER eurobase_owner;
SQL
echo "dedicated: eurobase_owner + database eurobase ready"

cd "$REPO_ROOT"
TEAM_TEST_SHARED_ADMIN="postgres://postgres:postgres@localhost:$SH_PORT/eurobase?sslmode=disable" \
TEAM_TEST_SHARED_GATEWAY="postgres://eurobase_gateway:localdev@localhost:$SH_PORT/eurobase?sslmode=disable" \
TEAM_TEST_SHARED_DEVELOPER="postgres://eurobase_developer:localdev@localhost:$SH_PORT/eurobase?sslmode=disable" \
TEAM_TEST_DED_OWNER="postgres://eurobase_owner:ownerpw@localhost:$DED_PORT/eurobase?sslmode=disable" \
TEAM_TEST_DED_ADMIN="postgres://postgres:postgres@localhost:$DED_PORT/eurobase?sslmode=disable" \
  go test ./internal/gateway/ -run TestTeamEndToEnd -count=1 -v "$@"
