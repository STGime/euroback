#!/usr/bin/env bash
# comp-beta-pro-projects.sh
#
# One-off: grandfather the pre-launch beta Pro projects that
# tested checkout under test-mode Mollie. Sets comped_until 12
# months out and adjusts legacy_pro_grace_until to comp_expiry+14d
# so they get a final grace window once comp lapses.
#
# Idempotent — safe to re-run. Refuses to touch a project that
# doesn't currently satisfy the "pre-launch beta Pro" shape
# (plan='pro' AND has NO active subscription) so a slip-up
# (wrong UUID, or the user has already upgraded properly) fails
# loud instead of quietly overwriting real state.
#
# Requires DATABASE_URL_DEVELOPER (the migrator-inheriting
# developer role — see CLAUDE.md § Postgres roles).
#
# Usage:
#   ./scripts/ops/comp-beta-pro-projects.sh \
#     --uuids <uuid1>,<uuid2> \
#     --until 2027-08-21 \
#     [--dry-run]

set -euo pipefail

UUIDS=""
UNTIL=""
DRY_RUN="false"
REASON="beta-tester grandfathered on billing flip 2026-08"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --uuids)   UUIDS="$2"; shift 2 ;;
        --until)   UNTIL="$2"; shift 2 ;;
        --reason)  REASON="$2"; shift 2 ;;
        --dry-run) DRY_RUN="true"; shift ;;
        *) echo "unknown arg: $1" >&2; exit 2 ;;
    esac
done

if [[ -z "$UUIDS" || -z "$UNTIL" ]]; then
    echo "usage: $0 --uuids <uuid1>,<uuid2> --until YYYY-MM-DD [--dry-run]" >&2
    exit 2
fi

if [[ -z "${DATABASE_URL_DEVELOPER:-}" ]]; then
    echo "DATABASE_URL_DEVELOPER not set (need the developer/migrator role)" >&2
    exit 2
fi

# Build the SQL IN-list from the comma-separated UUIDs. Quote each
# so the psql -v substitution stays a single arg.
IN_LIST=""
IFS=',' read -ra IDS <<< "$UUIDS"
for id in "${IDS[@]}"; do
    IN_LIST="${IN_LIST}'${id}'::uuid,"
done
IN_LIST="${IN_LIST%,}"  # trim trailing comma

read -r -d '' PREVIEW_SQL <<SQL || true
SELECT id, name, plan, legacy_pro_grace_until, comped_until, comped_reason
  FROM public.projects
 WHERE id IN (${IN_LIST});
SQL

echo "── Current state ──"
psql "$DATABASE_URL_DEVELOPER" -c "$PREVIEW_SQL"
echo

if [[ "$DRY_RUN" == "true" ]]; then
    echo "(dry-run — no writes)"
    exit 0
fi

read -r -p "Comp these projects until ${UNTIL} with reason '${REASON}'? [y/N] " ANSWER
if [[ ! "$ANSWER" =~ ^[yY]$ ]]; then
    echo "aborted"
    exit 1
fi

read -r -d '' UPDATE_SQL <<SQL || true
BEGIN;

-- Refuse if the project has an active/incomplete/past_due sub —
-- means the user has already added a real card and this script
-- is being run against the wrong UUID.
DO \$\$
DECLARE
    conflict_id UUID;
BEGIN
    SELECT p.id INTO conflict_id
      FROM public.projects p
     WHERE p.id IN (${IN_LIST})
       AND EXISTS (
           SELECT 1 FROM public.subscriptions s
            WHERE s.project_id = p.id
              AND s.status IN ('incomplete', 'active', 'past_due')
       )
     LIMIT 1;
    IF conflict_id IS NOT NULL THEN
        RAISE EXCEPTION 'project % has a live subscription — refusing to comp', conflict_id;
    END IF;
END\$\$;

UPDATE public.projects
   SET comped_until           = TIMESTAMPTZ '${UNTIL} 00:00:00+00',
       comped_reason          = '${REASON}',
       legacy_pro_grace_until = TIMESTAMPTZ '${UNTIL} 00:00:00+00' + interval '14 days'
 WHERE id IN (${IN_LIST})
   AND plan = 'pro';

SELECT id, name, plan, comped_until, comped_reason, legacy_pro_grace_until
  FROM public.projects
 WHERE id IN (${IN_LIST});

COMMIT;
SQL

psql "$DATABASE_URL_DEVELOPER" -v ON_ERROR_STOP=1 -c "$UPDATE_SQL"
echo "── Done ──"
