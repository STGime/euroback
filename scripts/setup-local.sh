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

echo "==> Running migrations..."
docker compose exec -T postgres psql -U eurobase_api -d eurobase < migrations/000001_platform_schema.up.sql 2>/dev/null || true
docker compose exec -T postgres psql -U eurobase_api -d eurobase < migrations/000002_tenant_functions.up.sql 2>/dev/null || true

echo ""
echo "==> Local dev environment is ready!"
echo ""
echo "Connection info:"
echo "  PostgreSQL: postgres://eurobase_api:localdev@localhost:5433/eurobase?sslmode=disable"
echo "  Redis:      redis://localhost:6380"
echo "  MinIO S3:   http://localhost:9000  (user: minioadmin / pass: minioadmin)"
echo "  MinIO UI:   http://localhost:9001"
echo ""
echo "Set environment variables:"
echo "  source .env.local"
