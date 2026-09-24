#!/usr/bin/env bash
# Replay EVERY migration, in order, on a fresh PostgreSQL 16 database —
# proves a new environment / DR rebuild / CI database can be built from
# migrations/ alone (#632). Uses scripts/db/apply-migrations.sh, which
# mirrors the production role history and runs the same golang-migrate
# CLI image as the prod migrate Job.
#
#   scripts/replay-migrations.sh              throwaway docker container
#   PGURL=postgres://… scripts/replay-migrations.sh
#                                             an existing EMPTY database;
#                                             PGURL must be a superuser
#                                             other than eurobase_api (CI)
set -euo pipefail

REPO_ROOT="$(git -C "$(dirname "$0")" rev-parse --show-toplevel)"

if [ -z "${PGURL:-}" ]; then
    CNAME=eb-migration-replay
    PORT="${REPLAY_PORT:-5462}"
    docker rm -f "$CNAME" >/dev/null 2>&1 || true
    docker run -d --name "$CNAME" \
        -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=eurobase \
        -p "$PORT:5432" postgres:16-alpine >/dev/null
    trap 'docker rm -f "$CNAME" >/dev/null 2>&1 || true' EXIT
    for _ in $(seq 1 30); do
        docker exec "$CNAME" pg_isready -U postgres -d eurobase >/dev/null 2>&1 && break
        sleep 1
    done
    sleep 1
    PGURL="postgres://postgres:postgres@localhost:$PORT/eurobase?sslmode=disable"
fi

"$REPO_ROOT/scripts/db/apply-migrations.sh" "$PGURL"

# A second run must be a clean no-op (setup-local.sh reruns rely on it).
echo "── Re-run (must be a no-op) ──"
"$REPO_ROOT/scripts/db/apply-migrations.sh" "$PGURL"

echo "Replay OK: every migration applies cleanly on a fresh PostgreSQL 16, and re-running is a no-op."
