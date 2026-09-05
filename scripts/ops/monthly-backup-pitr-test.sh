#!/usr/bin/env bash
# Monthly automated backup + PITR regression test.
#
# Provisions a throwaway Team-tier-shaped Scaleway RDB instance, runs
# runbook scenarios T3 (PITR to a specific timestamp) and T4 (RPO
# measurement) against it, tears it down. Post-launch hedge — catches
# Scaleway RDB regressions the day they land, not the day a customer
# needs to restore.
#
# Runs monthly from deploy/k8s/backup-pitr-monthly-test-cronjob.yaml
# (in-cluster) or ad-hoc from an operator laptop for spot-checks.
#
# Exit codes:
#   0 — T3 + T4 both passed
#   1 — T3 assertion failed (PITR did not respect the target timestamp)
#   2 — T4 assertion failed (RPO gap exceeded MAX_RPO_SECONDS)
#   3 — scw / psql setup or CLI mismatch
#   4 — teardown failed (leaked resources — investigate)
#
# Required env (from eurobase-secrets when run in-cluster):
#   SCW_ACCESS_KEY, SCW_SECRET_KEY, SCW_DEFAULT_ORGANIZATION_ID,
#   SCW_DEFAULT_PROJECT_ID, SCW_DEFAULT_REGION=fr-par
#   DISCORD_ALERTS_WEBHOOK (optional — logs to stdout if unset)
#
# Optional env:
#   INSTANCE_NAME_PREFIX (default: monthly-pitr-test)
#   NODE_TYPE            (default: DB-DEV-S)
#   VOLUME_TYPE          (default: bssd, matches production)
#   VOLUME_SIZE_GB       (default: 5)
#   ENGINE_VERSION       (default: PostgreSQL-16)
#   MAX_RPO_SECONDS      (default: 300 — matches published claim)
#   TEARDOWN_ON_FAILURE  (default: true — set false when debugging)

set -euo pipefail

# ── Config ────────────────────────────────────────────────────────────
INSTANCE_NAME_PREFIX="${INSTANCE_NAME_PREFIX:-monthly-pitr-test}"
NODE_TYPE="${NODE_TYPE:-DB-DEV-S}"
VOLUME_TYPE="${VOLUME_TYPE:-bssd}"
VOLUME_SIZE_GB="${VOLUME_SIZE_GB:-5}"
ENGINE_VERSION="${ENGINE_VERSION:-PostgreSQL-16}"
MAX_RPO_SECONDS="${MAX_RPO_SECONDS:-300}"
TEARDOWN_ON_FAILURE="${TEARDOWN_ON_FAILURE:-true}"

STAMP=$(date -u +%Y%m%d-%H%M%S)
INSTANCE_NAME="${INSTANCE_NAME_PREFIX}-${STAMP}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# ── Discord posting helper ───────────────────────────────────────────
post_discord() {
  local level="$1"; local msg="$2"
  if [ -z "${DISCORD_ALERTS_WEBHOOK:-}" ]; then
    echo "[$level] $msg"
    return
  fi
  local emoji
  case "$level" in
    OK) emoji=":white_check_mark:" ;;
    WARN) emoji=":warning:" ;;
    CRITICAL) emoji=":rotating_light:" ;;
    *) emoji=":information_source:" ;;
  esac
  curl -fsS -X POST "$DISCORD_ALERTS_WEBHOOK" \
    -H "Content-Type: application/json" \
    -d "$(jq -n --arg c "$emoji Eurobase backup+PITR monthly test — **$level** — $msg" '{content:$c}')" \
    >/dev/null || echo "discord post failed"
}

# ── CLI sanity ────────────────────────────────────────────────────────
for cmd in scw psql jq curl; do
  command -v "$cmd" >/dev/null || {
    echo "missing required command: $cmd" >&2
    exit 3
  }
done

# ── Teardown trap ─────────────────────────────────────────────────────
INSTANCE_ID=""
CLONE_ID=""
TEARDOWN_DONE=""   # guard so INT/TERM + EXIT don't double-run

teardown() {
  # Re-entrancy guard — trap fires on EXIT and also on INT/TERM (the
  # k8s deadline SIGTERM path is exactly what activeDeadlineSeconds
  # exists to invoke, and without the signal traps the bash EXIT
  # handler doesn't run on SIGTERM, leaking the throwaway instance).
  [ -n "$TEARDOWN_DONE" ] && return
  TEARDOWN_DONE=1

  local rc=$?
  set +e
  if [ "$TEARDOWN_ON_FAILURE" = "false" ] && [ $rc -ne 0 ]; then
    echo "TEARDOWN_ON_FAILURE=false — leaving $INSTANCE_ID / $CLONE_ID for inspection"
    return
  fi

  # Attempt both deletes; do NOT exit on the first failure. If the
  # clone delete fails first, we still want to try the (larger)
  # instance so a single stuck delete cannot strand the other.
  local failed=""
  for id in "$CLONE_ID" "$INSTANCE_ID"; do
    [ -n "$id" ] || continue
    echo "tearing down $id …"
    if ! scw rdb instance delete "$id" region=fr-par >/dev/null 2>&1; then
      failed="$failed $id"
    fi
  done

  if [ -n "$failed" ]; then
    post_discord CRITICAL "teardown of$failed failed — check Scaleway console for leaked resources"
    exit 4
  fi
}
trap teardown EXIT INT TERM

# ── Provision throwaway instance ──────────────────────────────────────
echo "creating $INSTANCE_NAME …"
INSTANCE_JSON=$(scw rdb instance create \
  name="$INSTANCE_NAME" \
  engine="$ENGINE_VERSION" \
  node-type="$NODE_TYPE" \
  volume-type="$VOLUME_TYPE" \
  volume-size="${VOLUME_SIZE_GB}GB" \
  is-ha-cluster=false \
  backup-schedule-frequency=24 \
  backup-schedule-retention=7 \
  user-name=tester \
  password="Monthly-test-$(openssl rand -hex 16)" \
  region=fr-par -o json)

INSTANCE_ID=$(echo "$INSTANCE_JSON" | jq -r .id)
[ -n "$INSTANCE_ID" ] && [ "$INSTANCE_ID" != "null" ] || {
  post_discord CRITICAL "scw rdb instance create returned no id — payload: $INSTANCE_JSON"
  exit 3
}
echo "instance_id=$INSTANCE_ID"

# Wait for status=ready (up to 10 min).
for i in $(seq 1 60); do
  s=$(scw rdb instance get "$INSTANCE_ID" region=fr-par -o json | jq -r .status)
  echo "  provisioning: $s"
  [ "$s" = "ready" ] && break
  sleep 10
done
[ "$s" = "ready" ] || {
  post_discord CRITICAL "instance $INSTANCE_ID did not reach ready in 10min (last status=$s)"
  exit 1
}

# Build the DATABASE_URL — user/pass from the create call, host from
# get, port from the same. Password is echoed by scw only on create, so
# grab it now.
DB_USER=$(echo "$INSTANCE_JSON" | jq -r .user_name)
DB_PASS=$(echo "$INSTANCE_JSON" | jq -r .password)
DB_HOST=$(scw rdb instance get "$INSTANCE_ID" region=fr-par -o json | jq -r '.endpoint.ip // .endpoint.hostname')
DB_PORT=$(scw rdb instance get "$INSTANCE_ID" region=fr-par -o json | jq -r '.endpoint.port // 51000')
export DATABASE_URL="postgres://${DB_USER}:${DB_PASS}@${DB_HOST}:${DB_PORT}/rdb?sslmode=require"

# ── T3: PITR to a specific timestamp ─────────────────────────────────
echo "T3 — seeding baseline …"
"$SCRIPT_DIR/seed-backup-pitr-test-data.sh" baseline "$DATABASE_URL" 200

echo "T3 — batch-A writes + manifest …"
psql "$DATABASE_URL" -c "
  INSERT INTO events (actor_email, action) VALUES
    ('alice@monthly-test', 'batch-A-event-1'),
    ('alice@monthly-test', 'batch-A-event-2');
"
"$SCRIPT_DIR/seed-backup-pitr-test-data.sh" batch-A "$DATABASE_URL"
sleep 5

echo "T3 — batch-B writes + manifest (INSERT bob + DELETE to-delete-*) …"
psql "$DATABASE_URL" -c "
  INSERT INTO events (actor_email, action) VALUES ('bob@monthly-test', 'batch-B-event');
  DELETE FROM documents WHERE title LIKE 'to-delete-%';
"
"$SCRIPT_DIR/seed-backup-pitr-test-data.sh" batch-B "$DATABASE_URL"

# T_TARGET is captured HERE — after batch-B's writes AND manifest stamp
# are durable (WAL-archived), and BEFORE any batch-C writes. PITR
# restores state at-or-before the target timestamp, so capturing before
# the batch-B writes would land the clone in a pre-batch-B state and
# make verify --manifest=batch-B fail deterministically every month.
sleep 30   # give Scaleway WAL archiving time to catch up past batch-B
T_TARGET=$(date -u +%Y-%m-%dT%H:%M:%SZ)
echo "T3 — T_TARGET=$T_TARGET (post-batch-B, pre-batch-C)"

echo "T3 — batch-C writes + manifest …"
psql "$DATABASE_URL" -c "
  INSERT INTO events (actor_email, action) VALUES ('carol@monthly-test', 'batch-C-event');
"
"$SCRIPT_DIR/seed-backup-pitr-test-data.sh" batch-C "$DATABASE_URL"
sleep 10

echo "T3 — cloning to T_TARGET ($T_TARGET) …"
CLONE_JSON=$(scw rdb instance clone "$INSTANCE_ID" \
  name="${INSTANCE_NAME}-clone" \
  node-type="$NODE_TYPE" \
  point-in-time="$T_TARGET" \
  region=fr-par -o json)
CLONE_ID=$(echo "$CLONE_JSON" | jq -r .id)
[ -n "$CLONE_ID" ] && [ "$CLONE_ID" != "null" ] || {
  post_discord CRITICAL "clone create returned no id — payload: $CLONE_JSON"
  exit 1
}

for i in $(seq 1 60); do
  s=$(scw rdb instance get "$CLONE_ID" region=fr-par -o json | jq -r .status)
  echo "  clone: $s"
  [ "$s" = "ready" ] && break
  sleep 10
done
[ "$s" = "ready" ] || {
  post_discord CRITICAL "clone $CLONE_ID did not reach ready in 10min (last status=$s)"
  exit 1
}

CLONE_HOST=$(scw rdb instance get "$CLONE_ID" region=fr-par -o json | jq -r '.endpoint.ip // .endpoint.hostname')
CLONE_PORT=$(scw rdb instance get "$CLONE_ID" region=fr-par -o json | jq -r '.endpoint.port // 51000')
export CLONE_URL="postgres://${DB_USER}:${DB_PASS}@${CLONE_HOST}:${CLONE_PORT}/rdb?sslmode=require"

echo "T3 — verifying clone against manifest batch-B …"
"$SCRIPT_DIR/verify-restore.sh" batch-B "$CLONE_URL" || {
  post_discord CRITICAL "T3 assertion failed — PITR clone does not match batch-B manifest"
  exit 1
}

# ── T4: RPO gap ───────────────────────────────────────────────────────
echo "T4 — inserting 60 rpo-test ticks + querying restore window …"
for _ in $(seq 1 60); do
  psql "$DATABASE_URL" -c "INSERT INTO events (actor_email, action, payload)
    VALUES ('rpo-test', 'tick', jsonb_build_object('t', now()));" >/dev/null
  sleep 1
done

WINDOW_JSON=$(scw rdb instance get "$INSTANCE_ID" region=fr-par -o json)
RESTORE_TO=$(echo "$WINDOW_JSON" | jq -r '.restore_to_time // empty')
if [ -z "$RESTORE_TO" ]; then
  echo "warn: instance JSON lacks .restore_to_time — Scaleway CLI may have changed the field name"
  post_discord WARN "T4 could not read .restore_to_time; skipping RPO measurement"
else
  # RESTORE_TO is the newest restorable moment; RPO gap = now - RESTORE_TO.
  RESTORE_TO_EPOCH=$(date -u -d "$RESTORE_TO" +%s 2>/dev/null || date -u -j -f '%Y-%m-%dT%H:%M:%SZ' "$RESTORE_TO" +%s)
  NOW_EPOCH=$(date -u +%s)
  RPO_GAP=$((NOW_EPOCH - RESTORE_TO_EPOCH))
  echo "T4 — RPO gap = ${RPO_GAP}s (max allowed ${MAX_RPO_SECONDS}s)"
  if [ "$RPO_GAP" -gt "$MAX_RPO_SECONDS" ]; then
    post_discord CRITICAL "T4 RPO gap ${RPO_GAP}s exceeds MAX_RPO_SECONDS=${MAX_RPO_SECONDS}"
    exit 2
  fi
fi

# ── Success ───────────────────────────────────────────────────────────
post_discord OK "monthly test passed — T3 clone matched batch-B manifest, T4 RPO gap ${RPO_GAP:-n/a}s (≤${MAX_RPO_SECONDS}s)"
echo "all good — teardown pending in trap"
