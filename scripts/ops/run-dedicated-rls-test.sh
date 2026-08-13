#!/usr/bin/env bash
# run-dedicated-rls-test.sh
#
# One-shot harness for the RLS-isolation regression suite in
# internal/dbprovider/rls_isolation_dedicated_test.go — the safety
# gate for flipping TEAM_TIER_ROUTING=1 in prod.
#
# The Go test only runs when TEST_DEDICATED_PGHOST + TEST_DEDICATED_OWNER_PW
# are set (needs a Scaleway-shape Postgres where eurobase_owner owns
# the DB). This script spins up an ephemeral local Postgres in Docker
# that mimics that shape, exports the env vars, runs the tests, then
# tears the container down.
#
# Why Docker instead of a real Scaleway RDB instance:
#   * Portable — anyone with Docker can run the safety gate without
#     paying for a managed-PG spin-up (2-5 min + euros per hour).
#   * Reproducible — every run starts from a fresh, empty PG so
#     tests can't accumulate cruft.
#   * Runnable in CI — a GitHub Actions job can use the same
#     docker-provider service and gate the flag-flip PR on it.
#
# The behavioural fidelity that matters is: eurobase_owner owns the DB
# (so provision_tenant's grants land correctly), and the connecting
# eurobase_owner role has CREATE ROLE (so bootstrap can create
# eurobase_gateway + eurobase_readonly). Docker's POSTGRES_USER makes
# the entrypoint role a superuser, which is stronger than Scaleway's
# eurobase_owner (which has CREATEROLE + CREATEDB but is not
# superuser). Superuser is a superset — nothing the test does breaks
# on that difference. If a future assertion depends on non-superuser
# behaviour, drop the entrypoint role's SUPERUSER attribute before
# running the tests.
#
# Usage:
#   ./scripts/ops/run-dedicated-rls-test.sh
#
# Env:
#   PG_IMAGE         — Postgres docker image (default: postgres:16)
#   PG_HOST_PORT     — Host port to bind the container to (default: 55432)
#   KEEP_CONTAINER   — Set to 1 to leave the container running after
#                      the tests (useful for debugging failures via
#                      psql). Default: unset (tear down on exit).

set -euo pipefail

# Colours only when stdout is a TTY (CI runners often aren't).
if [ -t 1 ]; then
    G=$'\033[32m'; R=$'\033[31m'; Y=$'\033[33m'; B=$'\033[1m'; N=$'\033[0m'
else
    G=""; R=""; Y=""; B=""; N=""
fi

: "${PG_IMAGE:=postgres:16}"
: "${PG_HOST_PORT:=55432}"

CONTAINER_NAME="eurobase-rls-test-$$"
OWNER_PASSWORD="rls-test-ephemeral-$(date +%s)"

# Tear down the container unless the operator asked to keep it.
cleanup() {
    local exit_code=$?
    if [ -n "${KEEP_CONTAINER:-}" ]; then
        printf "\n${Y}%s${N}\n" "KEEP_CONTAINER set — leaving ${CONTAINER_NAME} running on host port ${PG_HOST_PORT}."
        printf "  psql: PGPASSWORD='%s' psql -h localhost -p %s -U eurobase_owner -d rdb\n" "$OWNER_PASSWORD" "$PG_HOST_PORT"
        printf "  stop: docker rm -f %s\n" "$CONTAINER_NAME"
    else
        docker rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true
    fi
    return $exit_code
}
trap cleanup EXIT

info() { printf "\n${B}==>${N} %s\n" "$1"; }
ok()   { printf "  ${G}✓${N} %s\n" "$1"; }
fail() { printf "  ${R}✗ FAIL:${N} %s\n" "$1"; exit 1; }

command -v docker >/dev/null 2>&1 || fail "docker not on PATH — install Docker or point PATH at your engine"
command -v go     >/dev/null 2>&1 || fail "go not on PATH — need the Go toolchain to run the test binary"

# Fail early if the port is already in use — better than a cryptic
# docker "port already allocated" a few lines down.
if lsof -iTCP:${PG_HOST_PORT} -sTCP:LISTEN -Pn >/dev/null 2>&1; then
    fail "port ${PG_HOST_PORT} already in use; set PG_HOST_PORT to a free one"
fi

info "1. Starting ephemeral Postgres (${PG_IMAGE}) as eurobase_owner"
docker run -d --rm \
    --name "$CONTAINER_NAME" \
    -e POSTGRES_USER=eurobase_owner \
    -e POSTGRES_PASSWORD="$OWNER_PASSWORD" \
    -e POSTGRES_DB=rdb \
    -p "${PG_HOST_PORT}:5432" \
    "$PG_IMAGE" >/dev/null

ok "container ${CONTAINER_NAME} up on host port ${PG_HOST_PORT}"

info "2. Waiting for Postgres to accept connections"
# pg_isready inside the container — no local psql needed. Give it
# 30s (fresh init + WAL ~5s locally, longer on CI).
deadline=$(( $(date +%s) + 30 ))
while true; do
    if docker exec "$CONTAINER_NAME" pg_isready -U eurobase_owner -d rdb -q 2>/dev/null; then
        ok "postgres is ready"
        break
    fi
    if [ "$(date +%s)" -gt "$deadline" ]; then
        docker logs "$CONTAINER_NAME" 2>&1 | tail -20
        fail "postgres did not become ready within 30s"
    fi
    sleep 0.5
done

info "3. Running the RLS regression suite"
# TEST_DEDICATED_PGHOST expects host:port (setupDedicatedTest parses
# the last colon). TEST_DEDICATED_PGDB defaults to \"rdb\" inside the
# test but we set it explicitly for clarity.
TEST_DEDICATED_PGHOST="localhost:${PG_HOST_PORT}" \
TEST_DEDICATED_OWNER_PW="$OWNER_PASSWORD" \
TEST_DEDICATED_PGDB=rdb \
TEST_DEDICATED_REQUIRE=1 \
    go test ./internal/dbprovider/... -v -run TestRLS -count=1

# TEST_DEDICATED_REQUIRE=1 flips setupDedicatedTest from t.Skip to
# t.Fatal, so a broken container / bad env var here can't produce
# a green run that proves nothing. Local `go test ./...` on a laptop
# without this script keeps the friendly skip.

# `count=1` disables Go's test-result cache so a hidden regression
# in a `git bisect` doesn't get masked by a cached PASS from a
# previous commit against the same TEST_DEDICATED_* values.

info "${G}All RLS assertions passed${N}"
printf "\n${B}TEAM_TIER_ROUTING=1 safety gate: PASS${N}\n"
printf "Sign-off evidence for the flag-flip PR:\n"
printf "  * Test binary: %s\n" "$(go version)"
printf "  * PG image:    %s\n" "$PG_IMAGE"
printf "  * Container:   %s\n" "$CONTAINER_NAME"
printf "  * Ran at:      %s\n" "$(date -u +%FT%TZ)"
