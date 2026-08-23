#!/usr/bin/env bash
# backfill-scaleway-backup-expiry.sh — one-off (#458)
#
# Cleanup for the pre-#456 state: on-demand backups created BEFORE
# #456 landed (2026-08-23) sit at Scaleway with no expires_at,
# accumulating storage cost forever. #456 fixed the go-forward path
# (new on-demand backups carry expires_at = created + 30d) but did
# nothing for existing rows.
#
# This script PATCHes an expires_at onto each affected Scaleway
# backup. Investigation-first design — if the count is zero (likely
# early in Team-tier life), no run is needed; close #458 with a note.
#
# ── Two mutually-exclusive expiry modes ────────────────────────────
# The choice matters — it's an irreversible data-deletion decision
# on paying-customer backups, so it's a REQUIRED flag with NO
# default. Pick deliberately.
#
#   --mode created-plus-30d
#       Match #456's go-forward semantics exactly. Anything older
#       than 30 days vanishes on the next Scaleway reconcile after
#       we PATCH — could be days, not months. Only choose this if
#       you've sent affected customers a heads-up email; otherwise
#       it's silent deletion of their backups.
#
#   --mode now-plus-30d
#       Uniform 30-day grace window from cleanup date. Nothing is
#       deleted before N+30 days; a customer who wants to pull an
#       old backup has time. Recommended default for a paying-
#       customer cleanup.
#
# ── Prerequisites ──────────────────────────────────────────────────
#   Env:
#     DATABASE_URL_DEVELOPER    Platform DB (developer role; per
#                               CLAUDE.md the developer pool is what
#                               ops scripts use for platform reads).
#     SCW_SECRET_KEY            Scaleway API secret for the PATCH.
#     SCW_RDB_REGION            Scaleway region (default: fr-par).
#     PR456_MERGE_TS            Cutoff timestamp for "pre-#456"
#                               (default: 2026-08-23T14:04:23Z, the
#                               real merge time of PR #456; override
#                               only if a later merge changed it).
#
#   Binaries: psql, curl, awk, date.
#
# ── Wire-shape caveat ──────────────────────────────────────────────
# Scaleway RDB's backup-update endpoint is documented as
#   PATCH /rdb/v1/regions/{region}/backups/{backup_id}
# with body { "expires_at": "<RFC3339>" }. If a run against staging
# returns 404 / 405, check the current Scaleway API docs — the verb
# has historically been PATCH on backups but their action-style
# endpoints sometimes use POST /backups/{id}/action-name instead.
# One dry-run against staging + a spot-check via `curl -v` before
# a prod run.
#
# Usage:
#   ./scripts/ops/backfill-scaleway-backup-expiry.sh \
#     --mode now-plus-30d \
#     [--dry-run] \
#     [--limit N]

set -euo pipefail

MODE=""
DRY_RUN="false"
LIMIT=0    # 0 = no limit
PR456_MERGE_TS="${PR456_MERGE_TS:-2026-08-23T14:04:23Z}"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --mode)    MODE="$2"; shift 2 ;;
        --dry-run) DRY_RUN="true"; shift ;;
        --limit)   LIMIT="$2"; shift 2 ;;
        -h|--help)
            grep '^#' "$0" | sed 's/^# \{0,1\}//' | head -80
            exit 0
            ;;
        *) echo "unknown arg: $1" >&2; exit 2 ;;
    esac
done

if [[ "$MODE" != "created-plus-30d" && "$MODE" != "now-plus-30d" ]]; then
    echo "ERROR: --mode is REQUIRED and must be one of:" >&2
    echo "        created-plus-30d   (matches #456; may delete old backups immediately)" >&2
    echo "        now-plus-30d       (uniform 30-day grace from cleanup; recommended)" >&2
    exit 2
fi

if [[ -z "${DATABASE_URL_DEVELOPER:-}" ]]; then
    echo "DATABASE_URL_DEVELOPER not set — need the developer pool for platform reads" >&2
    exit 2
fi

if [[ -z "${SCW_SECRET_KEY:-}" ]]; then
    if [[ "$DRY_RUN" == "true" ]]; then
        echo "SCW_SECRET_KEY not set — proceeding in dry-run (no Scaleway calls)" >&2
    else
        echo "SCW_SECRET_KEY not set — required for the PATCH calls (set or use --dry-run)" >&2
        exit 2
    fi
fi

SCW_RDB_REGION="${SCW_RDB_REGION:-fr-par}"
SCW_API_BASE="https://api.scaleway.com"

# ── Fetch the list from the platform DB ────────────────────────────
# Filters:
#   * kind = 'ondemand' (scheduled backups are #457's territory —
#     they'll get their expiries via the reconcile sweeper).
#   * created_at < PR456_MERGE_TS — the discriminator for "pre-fix."
#
# NOTE: backup_snapshots.expires_at is TIMESTAMPTZ NOT NULL, so we
# can't filter on IS NULL — the cache always holds SOMETHING for
# every row. What we're targeting is Scaleway's expires_at, which
# is the field the PATCH actually changes. Our cache row's expires_at
# will be corrected by the next backup_sweeper tick after Scaleway's
# response.
LIMIT_CLAUSE=""
if [[ "$LIMIT" -gt 0 ]]; then
    LIMIT_CLAUSE="LIMIT $LIMIT"
fi

read -r -d '' LIST_SQL <<SQL || true
SELECT bs.provider_snapshot_id, bs.created_at, bs.project_id, bs.name
  FROM public.backup_snapshots bs
 WHERE bs.kind = 'ondemand'
   AND bs.created_at < TIMESTAMPTZ '$PR456_MERGE_TS'
 ORDER BY bs.created_at ASC
 $LIMIT_CLAUSE;
SQL

echo "── Investigation: pre-#456 on-demand backups (cutoff $PR456_MERGE_TS) ──"
psql "$DATABASE_URL_DEVELOPER" -v ON_ERROR_STOP=1 -F ' | ' -A -c "$LIST_SQL" > /tmp/backfill-list.$$.tsv
COUNT=$(($(wc -l < /tmp/backfill-list.$$.tsv) - 2)) # header + trailing "(N rows)"
if [[ "$COUNT" -lt 0 ]]; then COUNT=0; fi
echo "  → matched $COUNT rows"

if [[ "$COUNT" -eq 0 ]]; then
    echo "  → nothing to backfill; close #458 with 'zero pre-fix backups' and done."
    rm -f /tmp/backfill-list.$$.tsv
    exit 0
fi

echo
echo "First 10 rows (backup_id | created_at | project | name):"
head -12 /tmp/backfill-list.$$.tsv
echo
echo "Mode:            $MODE"
echo "Dry-run:         $DRY_RUN"
echo "Scaleway region: $SCW_RDB_REGION"

if [[ "$DRY_RUN" == "true" ]]; then
    echo
    echo "(dry-run — no Scaleway calls will be made)"
    rm -f /tmp/backfill-list.$$.tsv
    exit 0
fi

echo
echo "This will PATCH $COUNT Scaleway backup(s) to set expires_at."
if [[ "$MODE" == "created-plus-30d" ]]; then
    echo "Mode created-plus-30d will DELETE backups older than 30 days"
    echo "on the next Scaleway reconcile — this is irreversible."
    echo "Confirm affected customers have been notified."
fi
read -r -p "Proceed? [y/N] " CONFIRM
if [[ ! "$CONFIRM" =~ ^[yY]$ ]]; then
    echo "aborted"
    rm -f /tmp/backfill-list.$$.tsv
    exit 1
fi

# ── PATCH loop ─────────────────────────────────────────────────────
# 1 req/sec to stay well under Scaleway's rate limits. Idempotent —
# re-patching an already-set expires_at is a no-op at Scaleway.
UPDATED=0
FAILED=0

# Skip psql's header (line 1) + trailing "(N rows)" summary.
tail -n +2 /tmp/backfill-list.$$.tsv | head -n -1 | while IFS=' | ' read -r BACKUP_ID CREATED_AT PROJECT_ID NAME; do
    # Skip empty lines that can arise from the header block.
    [[ -z "$BACKUP_ID" ]] && continue

    # Compute expiry based on --mode.
    if [[ "$MODE" == "created-plus-30d" ]]; then
        # RFC3339, +30d from created_at. `date -d` is GNU-only;
        # macOS date needs -j -f. This script targets a Linux ops
        # box (per scripts/ops/ pattern), so GNU semantics apply.
        NEW_EXPIRY=$(date -u -d "$CREATED_AT + 30 days" +'%Y-%m-%dT%H:%M:%SZ')
    else
        NEW_EXPIRY=$(date -u -d "+30 days" +'%Y-%m-%dT%H:%M:%SZ')
    fi

    URL="$SCW_API_BASE/rdb/v1/regions/$SCW_RDB_REGION/backups/$BACKUP_ID"
    BODY=$(printf '{"expires_at":"%s"}' "$NEW_EXPIRY")

    HTTP_STATUS=$(curl -s -o /tmp/backfill-resp.$$ -w '%{http_code}' \
        -X PATCH "$URL" \
        -H "X-Auth-Token: $SCW_SECRET_KEY" \
        -H "Content-Type: application/json" \
        -d "$BODY")

    if [[ "$HTTP_STATUS" =~ ^2 ]]; then
        UPDATED=$((UPDATED + 1))
        echo "  ✓ $BACKUP_ID → expires_at=$NEW_EXPIRY"
    else
        FAILED=$((FAILED + 1))
        echo "  ✗ $BACKUP_ID → HTTP $HTTP_STATUS: $(cat /tmp/backfill-resp.$$)" >&2
    fi
    rm -f /tmp/backfill-resp.$$
    sleep 1
done

echo
echo "── Done ──"
echo "  Updated: $UPDATED"
echo "  Failed:  $FAILED"
if [[ "$FAILED" -gt 0 ]]; then
    echo "  Re-run to retry (idempotent — already-set rows are no-ops on Scaleway)."
fi

rm -f /tmp/backfill-list.$$.tsv
