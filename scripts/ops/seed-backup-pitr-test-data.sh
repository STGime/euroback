#!/usr/bin/env bash
# Seed a Scaleway RDB instance with test data for the backup + PITR
# validation runbook (docs/runbooks/backup-pitr-test.md).
#
# Creates 4 tables (users, documents, events, test_manifest), inserts a
# scaled seed (default 1,000 / 5,000 / 20,000 rows), and stamps a
# `test_manifest` row so verify-restore.sh can prove which state the
# restored DB landed in.
#
# Usage:
#   scripts/ops/seed-backup-pitr-test-data.sh <checkpoint-name> [db-url] [scale]
#
# Examples:
#   # Fresh baseline seed with default scale
#   DATABASE_URL=postgres://... scripts/ops/seed-backup-pitr-test-data.sh baseline
#
#   # Incremental mutation batch (assumes tables exist)
#   scripts/ops/seed-backup-pitr-test-data.sh batch-A
#
#   # Small scale (for T5 RTO measurement — small tier)
#   scripts/ops/seed-backup-pitr-test-data.sh baseline "$DATABASE_URL" 100
#
# Exits:
#   0 — checkpoint stamped
#   1 — psql / DB connect error
#   2 — usage error

set -euo pipefail

if [ $# -lt 1 ]; then
  echo "usage: $0 <checkpoint-name> [db-url] [scale]" >&2
  exit 2
fi

CHECKPOINT="$1"
DB_URL="${2:-${DATABASE_URL:-}}"
SCALE="${3:-1000}"

if [ -z "$DB_URL" ]; then
  echo "DATABASE_URL not set and no db-url argument given" >&2
  exit 2
fi

# ── Idempotent schema ────────────────────────────────────────────────
# Runs on every invocation; CREATE TABLE IF NOT EXISTS keeps repeated
# runs a no-op.

psql "$DB_URL" -v ON_ERROR_STOP=1 <<'SQL'
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

CREATE TABLE IF NOT EXISTS users (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  email TEXT UNIQUE NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS documents (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  owner_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  title TEXT NOT NULL,
  body TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS events (
  id BIGSERIAL PRIMARY KEY,
  actor_email TEXT NOT NULL,
  action TEXT NOT NULL,
  at TIMESTAMPTZ NOT NULL DEFAULT now(),
  payload JSONB
);

CREATE TABLE IF NOT EXISTS test_manifest (
  checkpoint_name TEXT PRIMARY KEY,
  taken_at TIMESTAMPTZ NOT NULL,
  row_count_users BIGINT NOT NULL,
  row_count_documents BIGINT NOT NULL,
  row_count_events BIGINT NOT NULL,
  checksum_documents TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS ix_documents_owner ON documents(owner_id);
CREATE INDEX IF NOT EXISTS ix_events_at ON events(at);
SQL

# ── Seed body ─────────────────────────────────────────────────────────
# For the baseline checkpoint, insert the full scaled dataset.
# For any other checkpoint, treat it as an incremental mutation and
# only stamp the manifest — mutation content is caller-driven (the
# runbook's T3 scenario runs its own INSERT/DELETE between checkpoints).

if [ "$CHECKPOINT" = "baseline" ]; then
  # Baseline is NOT idempotent: a re-run would duplicate `documents`
  # and `events` (which have no natural conflict key) and
  # ON CONFLICT DO UPDATE on `test_manifest` would silently overwrite
  # the good checksum with the corrupted one. Hard-fail instead of
  # silently corrupting the reference state.
  existing=$(psql "$DB_URL" -tA -c "
    SELECT (SELECT count(*) FROM users) + (SELECT count(*) FROM documents)
  ")
  if [ "$existing" -gt 0 ]; then
    echo "refusing to re-seed baseline: users+documents already contain $existing rows." >&2
    echo "if this is intentional, TRUNCATE users, documents, events, test_manifest first." >&2
    exit 1
  fi

  echo "seeding baseline dataset: ${SCALE} users, $((SCALE * 5)) documents (incl. 100 'to-delete-*'), $((SCALE * 20)) events…"

  # Users
  psql "$DB_URL" -v ON_ERROR_STOP=1 -v n="$SCALE" <<'SQL'
INSERT INTO users (email)
SELECT 'user-' || gs::text || '@test'
FROM generate_series(1, :n) gs;
SQL

  # Documents — 5 per user, deterministic body content so the checksum
  # is reproducible on restore.
  psql "$DB_URL" -v ON_ERROR_STOP=1 -v n="$SCALE" <<'SQL'
INSERT INTO documents (owner_id, title, body)
SELECT
  u.id,
  'doc-' || u.email || '-' || d.n,
  'body-' || u.email || '-' || d.n || '-lorem-ipsum'
FROM (SELECT id, email FROM users LIMIT :n) u,
     LATERAL generate_series(1, 5) d(n);
SQL

  # 100 explicitly-named documents that the T3 PITR scenario will
  # DELETE at T_B — the assertion "documents deleted at T_B are absent
  # on the clone restored to T_B" only proves something if such
  # documents actually exist in the seeded state.
  psql "$DB_URL" -v ON_ERROR_STOP=1 <<'SQL'
INSERT INTO documents (owner_id, title, body)
SELECT
  (SELECT id FROM users ORDER BY email LIMIT 1),
  'to-delete-' || d.n,
  'to-delete-body-' || d.n
FROM generate_series(1, 100) d(n);
SQL

  # Events — 20 per user, with staggered timestamps so PITR windows
  # can select subsets.
  psql "$DB_URL" -v ON_ERROR_STOP=1 -v n="$SCALE" <<'SQL'
INSERT INTO events (actor_email, action, at, payload)
SELECT
  u.email,
  'seed-action-' || e.n,
  now() - (e.n || ' minutes')::interval,
  jsonb_build_object('seq', e.n)
FROM (SELECT email FROM users LIMIT :n) u,
     LATERAL generate_series(1, 20) e(n);
SQL

fi

# ── Manifest stamp ────────────────────────────────────────────────────
# Always runs, for any checkpoint. Records row counts and a
# deterministic checksum over documents (id + body ordered by id) so
# verify-restore.sh can prove the DB state.

psql "$DB_URL" -v ON_ERROR_STOP=1 -v cp="$CHECKPOINT" <<'SQL'
INSERT INTO test_manifest (
  checkpoint_name, taken_at,
  row_count_users, row_count_documents, row_count_events,
  checksum_documents
)
SELECT
  :'cp', now(),
  (SELECT count(*) FROM users),
  (SELECT count(*) FROM documents),
  (SELECT count(*) FROM events),
  md5(coalesce(string_agg(d.id::text || d.body, ',' ORDER BY d.id), ''))
FROM documents d
ON CONFLICT (checkpoint_name) DO UPDATE SET
  taken_at = EXCLUDED.taken_at,
  row_count_users = EXCLUDED.row_count_users,
  row_count_documents = EXCLUDED.row_count_documents,
  row_count_events = EXCLUDED.row_count_events,
  checksum_documents = EXCLUDED.checksum_documents;
SQL

echo "checkpoint '$CHECKPOINT' stamped."
psql "$DB_URL" -c "SELECT * FROM test_manifest WHERE checkpoint_name = '$CHECKPOINT'"
