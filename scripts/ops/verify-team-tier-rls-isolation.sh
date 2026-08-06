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

# Passwords are passed to psql via PGPASSWORD (env), NOT via the URL,
# so they never appear in `ps aux` while the script runs. The URLs
# below carry only the username; each psql invocation sets
# PGPASSWORD inline for the specific role it's authenticating as.
OWNER_URL="postgres://eurobase_owner@${DEDICATED_HOST}:${DEDICATED_PORT}/${DEDICATED_DB}?sslmode=require"
RUNTIME_URL="postgres://eurobase_gateway@${DEDICATED_HOST}:${DEDICATED_PORT}/${DEDICATED_DB}?sslmode=require"

as_owner()   { PGPASSWORD="$OWNER_PASSWORD"   psql "$OWNER_URL"   "$@"; }
as_runtime() { PGPASSWORD="$RUNTIME_PASSWORD" psql "$RUNTIME_URL" "$@"; }

# UUIDs held stable across the run so the assertions can compare.
USER_A_ID="11111111-1111-1111-1111-111111111111"
USER_B_ID="22222222-2222-2222-2222-222222222222"

pass() { printf "  \033[32m✓\033[0m %s\n" "$1"; }
fail() { printf "  \033[31m✗ FAIL:\033[0m %s\n" "$1"; exit 1; }
info() { printf "\n\033[1m==>\033[0m %s\n" "$1"; }

# Cleanup trap: remove the seeded test users no matter how the
# script exits (fail-fast assertion, ^C, unexpected error). Prevents
# ops-test-a@ / ops-test-b@ from leaking into the tenant's real
# auth users table across runs. The delete is idempotent + safe on
# a partially-seeded state.
cleanup() {
    local exit_code=$?
    if [ -n "${SKIP_CLEANUP:-}" ]; then return $exit_code; fi
    as_owner -qc "
        DELETE FROM ${SCHEMA}.users
         WHERE id IN ('${USER_A_ID}', '${USER_B_ID}')
    " >/dev/null 2>&1 || true
    return $exit_code
}
trap cleanup EXIT

info "1. Verifying runtime role is NOT the table owner"
# If eurobase_gateway owns any tenant table, Postgres skips RLS
# for it and every subsequent assertion would pass for the wrong
# reason. Catch that up front.
owner_leak=$(as_owner -Atqc "
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
as_owner -qc "
    INSERT INTO ${SCHEMA}.users (id, email, display_name)
    VALUES ('${USER_A_ID}', 'ops-test-a@eurobase.app', 'Ops Test A'),
           ('${USER_B_ID}', 'ops-test-b@eurobase.app', 'Ops Test B')
    ON CONFLICT (id) DO NOTHING
"
pass "seeded users A + B in ${SCHEMA}.users"

info "3. Runtime role: anonymous SELECT returns zero rows"
anon_count=$(as_runtime -Atqc "
    SET LOCAL search_path TO ${SCHEMA}, public;
    SELECT count(*) FROM users
")
if [ "$anon_count" != "0" ]; then
    fail "anon SELECT returned ${anon_count} rows (expected 0). RLS is not enforced."
fi
pass "anon SELECT returned 0 rows"

info "4. Runtime role: SELECT as user-A returns only user-A"
a_row=$(as_runtime -Atqc "
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
b_row=$(as_runtime -Atqc "
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
service_count=$(as_runtime -Atqc "
    SET LOCAL search_path TO ${SCHEMA}, public;
    SELECT set_config('app.end_user_role', 'service', true);
    SELECT count(*) FROM users WHERE id IN ('${USER_A_ID}', '${USER_B_ID}')
")
if [ "$service_count" != "2" ]; then
    fail "service-role SELECT returned ${service_count} rows (expected 2). Service escape hatch broken."
fi
pass "service-role SELECT sees both test users"

# Cleanup handled by the EXIT trap — fires whether we reach here
# or exit early on a failed assertion. Keeps the tenant's real
# `users` table clean regardless of outcome.

printf "\n\033[32m✓ ALL CHECKS PASSED\033[0m — TEAM_TIER_ROUTING=1 is safe to flip for this project.\n"
