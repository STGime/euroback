#!/usr/bin/env bash
# verify-team-tier-rls-isolation.sh
#
# The safety gate for flipping TEAM_TIER_ROUTING=1 in prod (see
# issue #338). Proves — against a REAL Team-tier dedicated instance —
# that:
#
#   1. The runtime credential connects and authenticates
#      (eurobase_gateway / non-owner login).
#   2. RLS policies are ACTUALLY ENFORCED against runtime traffic:
#        * anonymous (no app.end_user_id set) sees zero users rows
#        * setting app.end_user_id = user-A returns only user-A's row
#        * setting app.end_user_id = user-B returns only user-B's row
#        * setting app.end_user_role = 'service' bypasses RLS (the
#          service-role escape hatch used by secret-key SDK calls)
#   3. The runtime role CANNOT bypass RLS by owning tables (a
#      sanity check that we didn't accidentally hand ownership to
#      eurobase_gateway).
#
# The RLS-bypass gap flagged in the M2.5 part 1 ultrareview is what
# these assertions guard against. If any check fails,
# TEAM_TIER_ROUTING must stay OFF — a green run is the pre-condition
# for the flip.
#
# Usage:
#   PROJECT_ID=<uuid> \
#   DEDICATED_HOST=<host> \
#   DEDICATED_PORT=5432 \
#   DEDICATED_DB=<name> \
#   OWNER_PASSWORD=<eurobase_owner password from vault> \
#   RUNTIME_PASSWORD=<eurobase_gateway password from vault> \
#     ./scripts/ops/verify-team-tier-rls-isolation.sh
#
# Owner + runtime passwords come from `project_databases` — decrypt
# with the platform Cipher (see internal/dbprovider/cipher.go).
# Ops helper for that lookup is a follow-up on #338.

set -euo pipefail

: "${PROJECT_ID:?PROJECT_ID required (Team-tier project UUID)}"
: "${DEDICATED_HOST:?DEDICATED_HOST required}"
: "${DEDICATED_PORT:=5432}"
: "${DEDICATED_DB:?DEDICATED_DB required}"
: "${OWNER_PASSWORD:?OWNER_PASSWORD required}"
: "${RUNTIME_PASSWORD:?RUNTIME_PASSWORD required}"

SCHEMA="tenant_${PROJECT_ID//-/_}"
OWNER_URL="postgres://eurobase_owner:${OWNER_PASSWORD}@${DEDICATED_HOST}:${DEDICATED_PORT}/${DEDICATED_DB}?sslmode=require"
RUNTIME_URL="postgres://eurobase_gateway:${RUNTIME_PASSWORD}@${DEDICATED_HOST}:${DEDICATED_PORT}/${DEDICATED_DB}?sslmode=require"

# UUIDs held stable across the run so the assertions can compare.
USER_A_ID="11111111-1111-1111-1111-111111111111"
USER_B_ID="22222222-2222-2222-2222-222222222222"

pass() { printf "  \033[32m✓\033[0m %s\n" "$1"; }
fail() { printf "  \033[31m✗ FAIL:\033[0m %s\n" "$1"; exit 1; }
info() { printf "\n\033[1m==>\033[0m %s\n" "$1"; }

info "1. Verifying runtime role is NOT the table owner"
# If eurobase_gateway owns any tenant table, Postgres skips RLS
# for it and every subsequent assertion would pass for the wrong
# reason. Catch that up front.
owner_leak=$(psql "$OWNER_URL" -Atqc "
    SELECT count(*)
      FROM pg_class c
      JOIN pg_namespace n ON n.oid = c.relnamespace
      JOIN pg_roles r ON r.oid = c.relowner
     WHERE n.nspname = '${SCHEMA}'
       AND c.relkind = 'r'
       AND r.rolname = 'eurobase_gateway'
")
if [ "$owner_leak" != "0" ]; then
    fail "eurobase_gateway owns ${owner_leak} tables in ${SCHEMA} — RLS would be bypassed"
fi
pass "eurobase_gateway owns zero tables in ${SCHEMA} (RLS applies)"

info "2. Seeding two test users as owner"
psql "$OWNER_URL" -qc "
    INSERT INTO ${SCHEMA}.users (id, email, display_name)
    VALUES ('${USER_A_ID}', 'ops-test-a@eurobase.app', 'Ops Test A'),
           ('${USER_B_ID}', 'ops-test-b@eurobase.app', 'Ops Test B')
    ON CONFLICT (id) DO NOTHING
"
pass "seeded users A + B in ${SCHEMA}.users"

info "3. Runtime role: anonymous SELECT returns zero rows"
anon_count=$(psql "$RUNTIME_URL" -Atqc "
    SET LOCAL search_path TO ${SCHEMA}, public;
    SELECT count(*) FROM users
")
if [ "$anon_count" != "0" ]; then
    fail "anon SELECT returned ${anon_count} rows (expected 0). RLS is not enforced."
fi
pass "anon SELECT returned 0 rows"

info "4. Runtime role: SELECT as user-A returns only user-A"
a_row=$(psql "$RUNTIME_URL" -Atqc "
    SET LOCAL search_path TO ${SCHEMA}, public;
    SELECT set_config('app.end_user_id', '${USER_A_ID}', true);
    SELECT set_config('app.end_user_role', 'authenticated', true);
    SELECT id::text FROM users
")
if [ "$a_row" != "$USER_A_ID" ]; then
    fail "user-A SELECT returned '${a_row}' (expected only ${USER_A_ID}). Cross-user leak."
fi
pass "user-A SELECT returned only user-A"

info "5. Runtime role: SELECT as user-B returns only user-B"
b_row=$(psql "$RUNTIME_URL" -Atqc "
    SET LOCAL search_path TO ${SCHEMA}, public;
    SELECT set_config('app.end_user_id', '${USER_B_ID}', true);
    SELECT set_config('app.end_user_role', 'authenticated', true);
    SELECT id::text FROM users
")
if [ "$b_row" != "$USER_B_ID" ]; then
    fail "user-B SELECT returned '${b_row}' (expected only ${USER_B_ID}). Cross-user leak."
fi
pass "user-B SELECT returned only user-B"

info "6. Runtime role: service-role escape hatch sees both users"
service_count=$(psql "$RUNTIME_URL" -Atqc "
    SET LOCAL search_path TO ${SCHEMA}, public;
    SELECT set_config('app.end_user_role', 'service', true);
    SELECT count(*) FROM users WHERE id IN ('${USER_A_ID}', '${USER_B_ID}')
")
if [ "$service_count" != "2" ]; then
    fail "service-role SELECT returned ${service_count} rows (expected 2). Service escape hatch broken."
fi
pass "service-role SELECT sees both test users"

info "7. Cleanup"
psql "$OWNER_URL" -qc "
    DELETE FROM ${SCHEMA}.users
     WHERE id IN ('${USER_A_ID}', '${USER_B_ID}')
"
pass "removed test users"

printf "\n\033[32m✓ ALL CHECKS PASSED\033[0m — TEAM_TIER_ROUTING=1 is safe to flip for this project.\n"
