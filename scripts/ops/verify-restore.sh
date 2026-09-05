#!/usr/bin/env bash
# Verify a restored/cloned Scaleway RDB instance against a named
# checkpoint stamped by seed-backup-pitr-test-data.sh.
#
# Compares live table state (users/documents/events row counts,
# documents md5 checksum) against the row in `test_manifest` for the
# named checkpoint. Prints per-metric pass/fail and exits accordingly.
#
# Usage:
#   scripts/ops/verify-restore.sh <checkpoint-name> [db-url]
#
# Examples:
#   # Verify a PITR clone lands at the batch-B state
#   DATABASE_URL=postgres://clone... scripts/ops/verify-restore.sh batch-B
#
# Exits:
#   0 — every metric matches
#   1 — one or more mismatches (prints diff table)
#   2 — usage error / checkpoint not found

set -euo pipefail

if [ $# -lt 1 ]; then
  echo "usage: $0 <checkpoint-name> [db-url]" >&2
  exit 2
fi

CHECKPOINT="$1"
DB_URL="${2:-${DATABASE_URL:-}}"

if [ -z "$DB_URL" ]; then
  echo "DATABASE_URL not set and no db-url argument given" >&2
  exit 2
fi

# Pull the expected values from the manifest row.
# Use psql -v binding (matches seed-*.sh) so the checkpoint name is
# passed as a bound parameter, not string-interpolated into SQL.
expected=$(psql "$DB_URL" -tA -F'|' -v cp="$CHECKPOINT" <<'SQL'
SELECT
  row_count_users,
  row_count_documents,
  row_count_events,
  checksum_documents,
  taken_at
FROM test_manifest
WHERE checkpoint_name = :'cp';
SQL
)

if [ -z "$expected" ]; then
  echo "checkpoint '$CHECKPOINT' not found in test_manifest — restored DB may not have been seeded" >&2
  exit 2
fi

IFS='|' read -r exp_users exp_docs exp_events exp_sum taken_at <<< "$expected"

# Query the live tables for actual state.
actual=$(psql "$DB_URL" -tA -F'|' <<'SQL'
SELECT
  (SELECT count(*) FROM users),
  (SELECT count(*) FROM documents),
  (SELECT count(*) FROM events),
  md5(coalesce((SELECT string_agg(id::text || body, ',' ORDER BY id) FROM documents), ''));
SQL
)

IFS='|' read -r act_users act_docs act_events act_sum <<< "$actual"

# Compare.
fail=0
printf "%-24s %-30s %-30s\n" "metric" "expected" "actual"
printf "%-24s %-30s %-30s\n" "------" "--------" "------"

check() {
  local label="$1" expected="$2" actual="$3"
  if [ "$expected" = "$actual" ]; then
    printf "%-24s %-30s %-30s ✓\n" "$label" "$expected" "$actual"
  else
    printf "%-24s %-30s %-30s ✗\n" "$label" "$expected" "$actual"
    fail=1
  fi
}

check "row_count_users"     "$exp_users"  "$act_users"
check "row_count_documents" "$exp_docs"   "$act_docs"
check "row_count_events"    "$exp_events" "$act_events"
check "checksum_documents"  "$exp_sum"    "$act_sum"

echo
if [ $fail -eq 0 ]; then
  echo "✓ checkpoint '$CHECKPOINT' (taken $taken_at) matches — restore verified."
  exit 0
else
  echo "✗ checkpoint '$CHECKPOINT' mismatch — restored DB is not in the expected state." >&2
  exit 1
fi
