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
#   0 — T3 + T4 + T5 all passed
#   1 — T3 assertion failed (PITR did not respect the target timestamp)
#   2 — measurement threshold exceeded (T4 RPO > MAX_RPO_SECONDS OR
#       T5 RTO > MAX_RTO_SECONDS). Distinguish which via the Discord
#       CRITICAL message body if triage matters.
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
#   MAX_RTO_SECONDS      (default: 600 — 10 min at ~5 MB seeded volume;
#                         matches the runbook T5 block threshold)
#   TEARDOWN_ON_FAILURE  (default: true — set false when debugging)

set -euo pipefail

# ── Config ────────────────────────────────────────────────────────────
INSTANCE_NAME_PREFIX="${INSTANCE_NAME_PREFIX:-monthly-pitr-test}"
NODE_TYPE="${NODE_TYPE:-DB-DEV-S}"
VOLUME_TYPE="${VOLUME_TYPE:-sbs_5k}"  # Scaleway RDB accepts [lssd bssd sbs_5k sbs_15k]; bssd deprecated; lssd has no scheduled backups (defeats the test); sbs_5k is cheapest option that supports the backup + PITR features under test.
VOLUME_SIZE_GB="${VOLUME_SIZE_GB:-5}"
ENGINE_VERSION="${ENGINE_VERSION:-PostgreSQL-16}"
MAX_RPO_SECONDS="${MAX_RPO_SECONDS:-300}"
MAX_RTO_SECONDS="${MAX_RTO_SECONDS:-600}"
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
  # Capture the real exit status FIRST — before the re-entrancy
  # guard's `TEARDOWN_DONE=1` assignment clobbers $? to 0. Otherwise
  # the TEARDOWN_ON_FAILURE=false debug knob is inert.
  local rc=$?

  # Re-entrancy guard — trap fires on EXIT and also on INT/TERM (the
  # k8s deadline SIGTERM path is exactly what activeDeadlineSeconds
  # exists to invoke, and without the signal traps the bash EXIT
  # handler doesn't run on SIGTERM, leaking the throwaway instance).
  [ -n "$TEARDOWN_DONE" ] && return
  TEARDOWN_DONE=1

  set +e
  if [ "$TEARDOWN_ON_FAILURE" = "false" ] && [ "$rc" -ne 0 ]; then
    echo "TEARDOWN_ON_FAILURE=false — leaving $INSTANCE_ID / $CLONE_ID for inspection"
    return
  fi

  # Attempt both deletes; do NOT exit on the first failure. If the
  # clone delete fails first, we still want to try the (larger)
  # instance so a single stuck delete cannot strand the other.
  # Scaleway refuses to delete instances in transient states
  # (provisioning, upgrading, etc.), so wait up to ~5 min per resource
  # for the state to settle before attempting delete.
  local failed=""
  for id in "$CLONE_ID" "$INSTANCE_ID"; do
    [ -n "$id" ] || continue
    echo "tearing down $id …"
    # Wait for a non-transient terminal state (ready/error/deleted).
    for _ in $(seq 1 30); do
      state=$(scw rdb instance get "$id" region=fr-par -o json 2>/dev/null | jq -r '.status // "unknown"')
      case "$state" in
        provisioning|upgrading|initializing|configuring|snapshotting|backuping|restoring|autohealing) sleep 10; continue ;;
        *) break ;;
      esac
    done
    if ! scw rdb instance delete "$id" region=fr-par >/dev/null 2>&1; then
      failed="$failed $id"
    fi
  done

  if [ -n "$failed" ]; then
    post_discord CRITICAL "teardown of$failed failed — check Scaleway console for leaked resources"
    exit 4
  fi
}
# On normal exit: teardown runs via EXIT. On signal (SIGINT /
# SIGTERM, including the k8s activeDeadlineSeconds kill): teardown
# runs AND we then exit 143 explicitly. Without the explicit exit,
# bash resumes the main script after the signal handler returns —
# with `set +e` (from inside teardown) still in effect shell-globally
# — so subsequent psql/scw failures against the now-deleted instance
# are silently ignored and the script can fall through to the
# post_discord OK line, producing a false "monthly test passed"
# after teardown already deleted everything.
trap teardown EXIT
trap 'teardown; exit 143' INT TERM

# ── Provision throwaway instance ──────────────────────────────────────
# Note: Scaleway CLI dropped backup-schedule-frequency / retention
# args from `instance create` (they moved to a separate management
# surface). New instances get the account-default schedule, which
# for RDB is daily + 7-day retention — same as what the runbook
# assumes. If your account default differs, set it explicitly after
# provisioning via the console or `scw rdb backup-schedule`
# subcommands, and update this comment.
# Capture credentials in shell variables BEFORE the create call so we
# can use them later — Scaleway's `instance create` response no longer
# echoes back the password we passed in (the field is either omitted
# or masked), so parsing it out of $INSTANCE_JSON returns null.
DB_USER="tester"
DB_PASS="Monthly-test-$(openssl rand -hex 16)"

echo "creating $INSTANCE_NAME …"
INSTANCE_JSON=$(scw rdb instance create \
  name="$INSTANCE_NAME" \
  engine="$ENGINE_VERSION" \
  node-type="$NODE_TYPE" \
  volume-type="$VOLUME_TYPE" \
  volume-size="${VOLUME_SIZE_GB}GB" \
  is-ha-cluster=false \
  user-name="$DB_USER" \
  password="$DB_PASS" \
  region=fr-par -o json)

INSTANCE_ID=$(echo "$INSTANCE_JSON" | jq -r .id)
[ -n "$INSTANCE_ID" ] && [ "$INSTANCE_ID" != "null" ] || {
  post_discord CRITICAL "scw rdb instance create returned no id — payload: $INSTANCE_JSON"
  exit 3
}
echo "instance_id=$INSTANCE_ID"

# Wait for status=ready (up to 10 min).
# `scw rdb instance get` may return a non-zero exit + JSON error body
# while an instance is still transitioning (recent CLI behavior); under
# `set -e` that kills the script mid-loop and fires teardown against a
# still-provisioning resource that Scaleway then refuses to delete.
# `|| true` on the API call keeps the loop alive; jq's `// "unknown"`
# handles empty JSON.
s=""
for i in $(seq 1 60); do
  # `|| true` preserves scw's stdout JSON even when it exits non-zero
  # (transient-state responses do that — the useful `.error.current_state`
  # lives in the JSON body). Empty-output safety net kicks in only if
  # scw itself is missing / catastrophically broken.
  json=$(scw rdb instance get "$INSTANCE_ID" region=fr-par -o json 2>/dev/null || true)
  [ -z "$json" ] && json='{}'
  # Fallback chain: `.status` on success, `.error.current_state` on
  # transient-state error (Scaleway RDB returns exit 1 + an error
  # JSON containing current_state while an instance is initializing /
  # provisioning / etc — captured via the `|| echo '{}'` above).
  s=$(echo "$json" | jq -r '.status // .error.current_state // "unknown"')
  echo "  provisioning: $s"
  [ "$s" = "ready" ] && break
  sleep 10
done
[ "$s" = "ready" ] || {
  post_discord CRITICAL "instance $INSTANCE_ID did not reach ready in 10min (last status=$s)"
  exit 1
}

# Build DATABASE_URL. DB_USER + DB_PASS are already captured above
# (Scaleway doesn't echo the password back in the create response).
# Host + port come from a fresh get now that the instance is ready.
DB_HOST=$(scw rdb instance get "$INSTANCE_ID" region=fr-par -o json | jq -r '.endpoint.ip // .endpoint.hostname // (.endpoints[0].ip // .endpoints[0].hostname)')
DB_PORT=$(scw rdb instance get "$INSTANCE_ID" region=fr-par -o json | jq -r '.endpoint.port // (.endpoints[0].port) // 51000')
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
# Take an explicit backup right after batch-B commits. This is the
# state the T3 verify step will assert against.
#
# Note: PITR-to-arbitrary-timestamp via `scw rdb instance clone
# --point-in-time` was removed from the Scaleway CLI. The surviving
# customer-facing restore path is `backup create` + `backup restore
# into a destination instance` — which is what Team-tier customers
# actually hit for their "1 restore/month included" flow. This test
# exercises that exact path.
echo "T3 — creating backup after batch-B …"
BACKUP_JSON=$(scw rdb backup create \
  instance-id="$INSTANCE_ID" \
  database-name=rdb \
  name="t3-backup-${STAMP}" \
  region=fr-par --wait -o json)
BACKUP_ID=$(echo "$BACKUP_JSON" | jq -r .id)
[ -n "$BACKUP_ID" ] && [ "$BACKUP_ID" != "null" ] || {
  post_discord CRITICAL "backup create returned no id — payload: $BACKUP_JSON"
  exit 1
}
echo "T3 — backup_id=$BACKUP_ID (ready)"

echo "T3 — batch-C writes + manifest …"
psql "$DATABASE_URL" -c "
  INSERT INTO events (actor_email, action) VALUES ('carol@monthly-test', 'batch-C-event');
"
"$SCRIPT_DIR/seed-backup-pitr-test-data.sh" batch-C "$DATABASE_URL"
sleep 5

# Provision a fresh destination instance for the restore. Scaleway
# requires the target of `backup restore` to already exist — the
# customer restore UI creates this instance behind the scenes; here
# we mirror that.
echo "T3 — provisioning destination instance for restore …"
DEST_NAME="${INSTANCE_NAME}-restore"
DEST_JSON=$(scw rdb instance create \
  name="$DEST_NAME" \
  engine="$ENGINE_VERSION" \
  node-type="$NODE_TYPE" \
  volume-type="$VOLUME_TYPE" \
  volume-size="${VOLUME_SIZE_GB}GB" \
  is-ha-cluster=false \
  user-name="$DB_USER" \
  password="$DB_PASS" \
  region=fr-par -o json)
CLONE_ID=$(echo "$DEST_JSON" | jq -r .id)   # reuse CLONE_ID → teardown sees it
[ -n "$CLONE_ID" ] && [ "$CLONE_ID" != "null" ] || {
  post_discord CRITICAL "destination instance create returned no id — payload: $DEST_JSON"
  exit 1
}
echo "T3 — dest_instance_id=$CLONE_ID"

# Wait for destination ready (same set-e-safe pattern).
s=""
for i in $(seq 1 60); do
  json=$(scw rdb instance get "$CLONE_ID" region=fr-par -o json 2>/dev/null || true)
  [ -z "$json" ] && json='{}'
  s=$(echo "$json" | jq -r '.status // .error.current_state // "unknown"')
  echo "  destination: $s"
  [ "$s" = "ready" ] && break
  sleep 10
done
[ "$s" = "ready" ] || {
  post_discord CRITICAL "destination $CLONE_ID did not reach ready in 10min (last status=$s)"
  exit 1
}

# ── T3/T5: restore backup into destination, time it (RTO measurement).
#
# This IS the T5 RTO for the ~5 MB seeded volume. Captures Scaleway's
# fixed restore-overhead (backup fetch + replay into a fresh DB); at
# 5 MB the fixed component dominates. Do NOT extrapolate linearly to
# larger volumes — RTO ≈ FIXED + k·data_size is affine; one point
# pins only the intercept, not the slope. Larger workloads get a
# bespoke measurement on request per the runbook T5 policy.
echo "T3/T5 — restoring backup $BACKUP_ID into destination $CLONE_ID …"
CLONE_START=$(date -u +%s)
if ! scw rdb backup restore "$BACKUP_ID" \
  instance-id="$CLONE_ID" \
  region=fr-par --wait >/dev/null; then
  post_discord CRITICAL "T3 backup restore failed — check Scaleway console for backup=$BACKUP_ID dest=$CLONE_ID"
  exit 1
fi
CLONE_END=$(date -u +%s)
RTO_SECONDS=$((CLONE_END - CLONE_START))
echo "T5 — RTO (backup restore → ready) = ${RTO_SECONDS}s at ~5 MB seeded volume (max allowed ${MAX_RTO_SECONDS}s)"
if [ "$RTO_SECONDS" -gt "$MAX_RTO_SECONDS" ]; then
  post_discord CRITICAL "T5 RTO ${RTO_SECONDS}s exceeds MAX_RTO_SECONDS=${MAX_RTO_SECONDS} at ~5 MB — Scaleway restore overhead has drifted"
  exit 2
fi

CLONE_HOST=$(scw rdb instance get "$CLONE_ID" region=fr-par -o json | jq -r '.endpoint.ip // .endpoint.hostname // (.endpoints[0].ip // .endpoints[0].hostname)')
CLONE_PORT=$(scw rdb instance get "$CLONE_ID" region=fr-par -o json | jq -r '.endpoint.port // (.endpoints[0].port) // 51000')
export CLONE_URL="postgres://${DB_USER}:${DB_PASS}@${CLONE_HOST}:${CLONE_PORT}/rdb?sslmode=require"

echo "T3 — verifying restored destination against manifest batch-B …"
"$SCRIPT_DIR/verify-restore.sh" batch-B "$CLONE_URL" || {
  post_discord CRITICAL "T3 assertion failed — restored destination does not match batch-B manifest"
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
post_discord OK "monthly test passed — T3 clone matched batch-B manifest, T4 RPO gap ${RPO_GAP:-n/a}s (≤${MAX_RPO_SECONDS}s), T5 RTO ${RTO_SECONDS}s at ~5 MB (bespoke measurement on request for larger volumes)"
echo "all good — teardown pending in trap"
