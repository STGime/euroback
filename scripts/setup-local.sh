#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

cd "$PROJECT_ROOT"

for bin in docker psql; do
    command -v "$bin" > /dev/null || { echo "ERROR: '$bin' not found on PATH (psql: brew install libpq && brew link --force libpq)"; exit 1; }
done

echo "==> Starting local dev services..."
docker compose up -d

echo "==> Waiting for PostgreSQL to be healthy..."
for i in $(seq 1 30); do
    if docker compose exec -T postgres pg_isready -U postgres -d eurobase > /dev/null 2>&1; then
        echo "    PostgreSQL is ready."
        break
    fi
    if [ "$i" -eq 30 ]; then
        echo "ERROR: PostgreSQL did not become ready in time."
        exit 1
    fi
    sleep 1
done

# Same path as production (#632): prod role shapes, the golang-migrate
# CLI, and a hard stop on the first failing migration. Re-running only
# applies what's new.
# Volumes created before #632 used eurobase_api as the built-in superuser
# and have no "postgres" role — they can't be migrated in place.
if ! docker compose exec -T postgres psql -U postgres -d eurobase -c 'SELECT 1' > /dev/null 2>&1; then
    echo "ERROR: this local database predates the #632 setup (no 'postgres' superuser)."
    echo "       Recreate it (deletes local data): docker compose down -v && scripts/setup-local.sh"
    exit 1
fi

echo "==> Running database migrations..."
"$SCRIPT_DIR/db/apply-migrations.sh" "postgres://postgres:postgres@localhost:5433/eurobase?sslmode=disable"

echo "==> Running River schema migrations..."
DATABASE_URL="postgres://eurobase_api:localdev@localhost:5433/eurobase?sslmode=disable"
"$(go env GOPATH)/bin/river" migrate-up --database-url "$DATABASE_URL" 2>/dev/null || {
    echo "    River CLI not found. Installing..."
    go install github.com/riverqueue/river/cmd/river@v0.31.0
    "$(go env GOPATH)/bin/river" migrate-up --database-url "$DATABASE_URL"
}

echo "==> Configuring Garage (local S3)..."
# Fixed local-only dev key (Garage key ids are GK + 24 hex, secrets 64 hex).
GARAGE_KEY_ID=GK0000000000000000000000de
GARAGE_SECRET=00000000000000000000000000000000000000000000000000000000000000de
garage() { docker compose exec -T -e RUST_LOG=warn garage /garage "$@"; }
for _ in $(seq 1 30); do garage status >/dev/null 2>&1 && break; sleep 1; done
garage status >/dev/null || { echo "Garage is not answering (docker compose logs garage)" >&2; exit 1; }
LAYOUT="$(garage layout show 2>/dev/null || true)"
if ! printf '%s' "$LAYOUT" | grep -q "Current cluster layout version: [1-9]"; then
    NODE="$(garage node id -q | cut -d@ -f1)"
    garage layout assign -z dc1 -c 1G "$NODE" >/dev/null
    garage layout apply --version 1 >/dev/null
fi
garage key info "$GARAGE_KEY_ID" >/dev/null 2>&1 || garage key import --yes -n local-dev "$GARAGE_KEY_ID" "$GARAGE_SECRET" >/dev/null
garage key allow --create-bucket "$GARAGE_KEY_ID" >/dev/null

echo ""
echo "==> Local dev environment is ready!"
echo ""
echo "Connection info:"
echo "  PostgreSQL: postgres://eurobase_api:localdev@localhost:5433/eurobase?sslmode=disable"
echo "  Redis:      redis://localhost:6380"
echo "  S3 (Garage): http://localhost:9000  region fr-par"
echo "    gateway: SCW_S3_ENDPOINT=http://localhost:9000 SCW_S3_REGION=fr-par"
echo "             SCW_ACCESS_KEY=$GARAGE_KEY_ID SCW_SECRET_KEY=$GARAGE_SECRET"
echo "    worker:  S3_ENDPOINT=http://localhost:9000 S3_REGION=fr-par"
echo "             S3_ACCESS_KEY=$GARAGE_KEY_ID S3_SECRET_KEY=$GARAGE_SECRET"
echo "    (an .env.local from the MinIO days has minioadmin keys / us-east-1 — update it)"
echo ""
echo "Start the services:"
echo "  1. Gateway:  source .env.local && go run ./cmd/gateway"
echo "  2. Worker:   source .env.local && go run ./cmd/worker"
echo "  3. Console:  cd console && npm run dev"
echo ""
