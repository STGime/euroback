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
S3=eb-team-s3
SH_PORT="${TEAM_SHARED_PORT:-5470}"
DED_PORT="${TEAM_DED_PORT:-5471}"
S3_PORT="${TEAM_S3_PORT:-5472}"

cleanup() {
  docker rm -f "$SH" "$DED" "$S3" >/dev/null 2>&1 || true
  for f in "${CERTS:-}" "${GARAGE_CONF:-}"; do [ -z "$f" ] || rm -rf "$f"; done
}
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
# S3 for the storage checks: Garage (EU, Deuxfleurs; MinIO no longer
# publishes community images). Single node, region fr-par like the client,
# a fixed throwaway key allowed to create buckets.
GARAGE_KEY_ID=GK0123456789abcdef01234567
GARAGE_SECRET=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
GARAGE_CONF="$(mktemp)"
cat >"$GARAGE_CONF" <<TOML
metadata_dir = "/var/lib/garage/meta"
data_dir = "/var/lib/garage/data"
db_engine = "sqlite"
replication_factor = 1
rpc_bind_addr = "[::]:3901"
rpc_public_addr = "127.0.0.1:3901"
rpc_secret = "$(openssl rand -hex 32)"
[s3_api]
s3_region = "fr-par"
api_bind_addr = "[::]:3900"
root_domain = ".s3.garage.localhost"
TOML
docker run -d --name "$S3" -p "$S3_PORT:3900" -v "$GARAGE_CONF:/etc/garage.toml:ro" \
  dxflrs/garage:v1.1.0@sha256:fdb8272fcbe643eef830ee17874d5d4ed623a86501f1acbac8012583113b1c26 >/dev/null
for c in "$SH" "$DED"; do
  ok=
  for _ in $(seq 1 30); do docker exec "$c" pg_isready -h 127.0.0.1 -U postgres >/dev/null 2>&1 && ok=1 && break; sleep 1; done
  [ -n "$ok" ] || { echo "$c: postgres not ready after 30s" >&2; docker logs "$c" | tail -20 >&2; exit 1; }
done

garage() { docker exec -e RUST_LOG=warn "$S3" /garage "$@"; }
ok=
for _ in $(seq 1 30); do garage status >/dev/null 2>&1 && ok=1 && break; sleep 1; done
[ -n "$ok" ] || { echo "$S3: garage not ready after 30s" >&2; docker logs "$S3" | tail -20 >&2; exit 1; }
NODE="$(garage node id -q | cut -d@ -f1)" && [ -n "$NODE" ] || { echo "$S3: can't read the garage node id" >&2; docker logs "$S3" | tail -20 >&2; exit 1; }
garage layout assign -z dc1 -c 1G "$NODE" >/dev/null
garage layout apply --version 1 >/dev/null
garage key import --yes -n team-e2e "$GARAGE_KEY_ID" "$GARAGE_SECRET" >/dev/null
garage key allow --create-bucket "$GARAGE_KEY_ID" >/dev/null
echo "s3: garage ready"
MIGLOG="$(mktemp)"
if ! "$REPO_ROOT/scripts/db/apply-migrations.sh" "postgres://postgres:postgres@localhost:$SH_PORT/eurobase?sslmode=disable" >"$MIGLOG" 2>&1; then
  cat "$MIGLOG"; exit 1
fi
tail -1 "$MIGLOG"; rm -f "$MIGLOG"

# The "dedicated" instance, shaped like Scaleway RDB: eurobase_owner is NOT
# a superuser (CREATEROLE + CREATEDB) and does NOT own the databases (the
# superuser does, like _rdb_superadmin); PUBLIC has no CONNECT, so the
# runtime roles only get in through the test's SetPrivilege stand-in. One
# database per scenario (fresh / upgraded).
docker exec -i "$DED" psql -U postgres -q -v ON_ERROR_STOP=1 <<'SQL'
CREATE ROLE eurobase_owner LOGIN PASSWORD 'ownerpw' CREATEROLE CREATEDB;
CREATE DATABASE eb_fresh;
CREATE DATABASE eb_upgraded;
REVOKE CONNECT ON DATABASE eb_fresh, eb_upgraded FROM PUBLIC;
GRANT ALL ON DATABASE eb_fresh, eb_upgraded TO eurobase_owner;
\c eb_fresh
GRANT ALL ON SCHEMA public TO eurobase_owner;
\c eb_upgraded
GRANT ALL ON SCHEMA public TO eurobase_owner;
SQL
echo "dedicated: eurobase_owner + databases eb_fresh, eb_upgraded ready"

cd "$REPO_ROOT"
TEAM_TEST_SHARED_ADMIN="postgres://postgres:postgres@localhost:$SH_PORT/eurobase?sslmode=disable" \
TEAM_TEST_SHARED_GATEWAY="postgres://eurobase_gateway:localdev@localhost:$SH_PORT/eurobase?sslmode=disable" \
TEAM_TEST_SHARED_DEVELOPER="postgres://eurobase_developer:localdev@localhost:$SH_PORT/eurobase?sslmode=disable" \
TEAM_TEST_DED_OWNER="postgres://eurobase_owner:ownerpw@localhost:$DED_PORT/eb_fresh" \
TEAM_TEST_DED_ADMIN="postgres://postgres:postgres@localhost:$DED_PORT/eb_fresh?sslmode=disable" \
TEAM_TEST_S3_ENDPOINT="http://localhost:$S3_PORT" TEAM_TEST_S3_KEY="$GARAGE_KEY_ID" TEAM_TEST_S3_SECRET="$GARAGE_SECRET" \
  go test ./internal/gateway/ -run TestTeamEndToEnd -count=1 -v "$@"
