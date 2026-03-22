#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

cd "$PROJECT_ROOT"

echo "==> Starting local dev services..."
docker compose up -d

echo "==> Waiting for PostgreSQL to be healthy..."
for i in $(seq 1 30); do
    if docker compose exec -T postgres pg_isready -U eurobase_api -d eurobase > /dev/null 2>&1; then
        echo "    PostgreSQL is ready."
        break
    fi
    if [ "$i" -eq 30 ]; then
        echo "ERROR: PostgreSQL did not become ready in time."
        exit 1
    fi
    sleep 1
done

echo "==> Running database migrations..."
for f in migrations/*.up.sql; do
    echo "    Applying $f..."
    docker compose exec -T postgres psql -U eurobase_api -d eurobase < "$f" 2>/dev/null || true
done

echo "==> Running River schema migrations..."
DATABASE_URL="postgres://eurobase_api:localdev@localhost:5433/eurobase?sslmode=disable"
"$(go env GOPATH)/bin/river" migrate-up --database-url "$DATABASE_URL" 2>/dev/null || {
    echo "    River CLI not found. Installing..."
    go install github.com/riverqueue/river/cmd/river@v0.31.0
    "$(go env GOPATH)/bin/river" migrate-up --database-url "$DATABASE_URL"
}

echo "==> Configuring MinIO alias..."
docker compose exec -T minio mc alias set local http://localhost:9000 minioadmin minioadmin 2>/dev/null || true

echo ""
echo "==> Local dev environment is ready!"
echo ""
echo "Connection info:"
echo "  PostgreSQL: postgres://eurobase_api:localdev@localhost:5433/eurobase?sslmode=disable"
echo "  Redis:      redis://localhost:6380"
echo "  MinIO S3:   http://localhost:9000  (user: minioadmin / pass: minioadmin)"
echo "  MinIO UI:   http://localhost:9001"
echo ""
echo "Start the services:"
echo "  1. Gateway:  source .env.local && go run ./cmd/gateway"
echo "  2. Worker:   source .env.local && go run ./cmd/worker"
echo "  3. Console:  cd console && npm run dev"
echo ""
