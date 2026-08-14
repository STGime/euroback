#!/usr/bin/env bash
# test-team-tier-instance.sh
#
# End-to-end validation for a Team-tier project's dedicated Scaleway
# instance + the SDK surface that sits on top of it.
#
# Does three orthogonal things:
#
#   1. **Backfill via Scaleway API.** Applies `readwrite` to
#      eurobase_gateway + `readonly` to eurobase_readonly via
#      Scaleway's PUT /rdb/v1/…/privileges endpoint (runs as their
#      `_rdb_superadmin` server-side, bypasses the ownership
#      limitation that makes SQL `GRANT CONNECT` a silent no-op
#      when the customer-visible eurobase_owner doesn't actually
#      own the `rdb` database).
#
#      Needed for instances provisioned before the code fix that
#      wires this into the ProvisionTeamDatabaseWorker path.
#
#   2. **Direct DB smoke.** psql as eurobase_readonly (RLS fence)
#      and eurobase_owner (round-trip), verifies tenant schema
#      shape + policies + PG version.
#
#   3. **SDK smoke.** curl against the project subdomain: data
#      CRUD, vault round-trip, end-user signup / signin / refresh —
#      the full A→D + #391 + #394 path.
#
# Env — no secrets in argv (they'd leak into ps):
#
#   OWNER_DSN            — postgres://eurobase_owner:PW@host:port/rdb?sslmode=require
#   READONLY_DSN         — postgres://eurobase_readonly:PW@host:port/rdb?sslmode=require
#   PROJECT_ID           — project UUID (derives tenant_<id> schema)
#   SLUG                 — project slug (SDK base: https://<slug>.eurobase.app)
#   PUBLIC_KEY           — eb_pk_… (SDK anon / signup)
#   SECRET_KEY           — eb_sk_… (SDK service-role writes)
#   PROVIDER_INSTANCE_ID — Scaleway RDB instance UUID (for the API grant)
#                          Look up in `project_databases.provider_instance_id`
#                          for this project's row.
#   SCW_SECRET_KEY       — Scaleway API secret key. Get from:
#                            kubectl -n eurobase get secret eurobase-secrets \
#                              -o jsonpath='{.data.SCW_SECRET_KEY}' | base64 -d
#                          If unset, step 1 is skipped (assumes grants already
#                          in place — good for a re-run against a project
#                          that's already fixed).
#   SCW_REGION           — Scaleway region (default: fr-par).
#
# Everything defaults to myteam3's values so a quick check is a
# single command; override any one to target a different Team-tier
# project.

set -uo pipefail

: "${OWNER_DSN:=postgres://eurobase_owner:-S.YwTkXSBKTTNB%3EtpzxFKx~%3E;D4;KkR@51.159.205.20:27697/rdb?sslmode=require}"
: "${READONLY_DSN:=postgres://eurobase_readonly:b666a78b4acd546efcb23c61b322cbf34b8e39a361078e4769230cb35aebd0f5@51.159.205.20:27697/rdb?sslmode=require}"
: "${PROJECT_ID:=19fac72b-68a1-4e88-9ea2-9fd68a94e200}"
: "${SLUG:=myteam3}"
: "${PUBLIC_KEY:=eb_pk_a5eecb0e86cd816d4f51ec1c8b1c2438}"
: "${SECRET_KEY:=eb_sk_25eecf6cf8c47f4d9638afce036f0ec6}"
: "${PROVIDER_INSTANCE_ID:=4aa613f3-e354-47eb-8da4-cec3765716df}"
: "${SCW_REGION:=fr-par}"
: "${SCW_SECRET_KEY:=}"

SCHEMA="tenant_${PROJECT_ID//-/_}"
BASE="https://${SLUG}.eurobase.app"

# Find psql — Homebrew keg-only libpq is the common shape on macOS
PSQL="${PSQL:-$(command -v psql 2>/dev/null)}"
if [ -z "$PSQL" ]; then
    for cand in \
        /opt/homebrew/opt/libpq/bin/psql \
        /usr/local/opt/libpq/bin/psql \
        /Applications/Postgres.app/Contents/Versions/latest/bin/psql; do
        [ -x "$cand" ] && { PSQL="$cand"; break; }
    done
fi
if [ -z "$PSQL" ] || [ ! -x "$PSQL" ]; then
    echo "FATAL: psql not on PATH — brew install libpq && brew link --force libpq"
    exit 1
fi
command -v curl >/dev/null || { echo "FATAL: curl not on PATH"; exit 1; }
command -v python3 >/dev/null || { echo "FATAL: python3 not on PATH (used for JSON pretty-print)"; exit 1; }

if [ -t 1 ]; then
    G=$'\033[32m'; R=$'\033[31m'; Y=$'\033[33m'; B=$'\033[1;36m'; N=$'\033[0m'
else G=""; R=""; Y=""; B=""; N=""; fi

section() { printf "\n${B}══ %s ══${N}\n" "$1"; }
ok()      { printf "  ${G}✓${N} %s\n" "$1"; }
warn()    { printf "  ${Y}⚠${N} %s\n" "$1"; }
fail()    { printf "  ${R}✗ FAIL:${N} %s\n" "$1"; exit 1; }
info()    { printf "    %s\n" "$1"; }

owner()     { "$PSQL" "$OWNER_DSN"    "$@"; }
readonly_() { "$PSQL" "$READONLY_DSN" "$@"; }

# ─────────────────────────────────────────────────────────────
section "1. Backfill CONNECT + DB privileges (via Scaleway API — runs as _rdb_superadmin)"

if [ -z "$SCW_SECRET_KEY" ]; then
    warn "SCW_SECRET_KEY not set — skipping the provider-side privilege grant"
    info "If step 2 fails with 'permission denied for database', set SCW_SECRET_KEY and re-run:"
    info "  export SCW_SECRET_KEY=\$(kubectl -n eurobase get secret eurobase-secrets -o jsonpath='{.data.SCW_SECRET_KEY}' | base64 -d)"
else
    # PUT /rdb/v1/regions/{region}/instances/{instance_id}/privileges
    # Body: {"database_name":"rdb", "user_name":"…", "permission":"…"}
    for pair in "eurobase_gateway:readwrite" "eurobase_readonly:readonly"; do
        USER="${pair%%:*}"
        PERM="${pair##*:}"
        HTTP=$(curl -s -o /tmp/scw-resp.$$ -w "%{http_code}" \
            -X PUT "https://api.scaleway.com/rdb/v1/regions/${SCW_REGION}/instances/${PROVIDER_INSTANCE_ID}/privileges" \
            -H "X-Auth-Token: $SCW_SECRET_KEY" \
            -H "Content-Type: application/json" \
            -d "{\"database_name\":\"rdb\",\"user_name\":\"${USER}\",\"permission\":\"${PERM}\"}")
        if [ "$HTTP" = "200" ] || [ "$HTTP" = "201" ]; then
            ok "granted ${USER} → ${PERM} on rdb (HTTP $HTTP)"
        else
            cat /tmp/scw-resp.$$
            rm -f /tmp/scw-resp.$$
            fail "SetPrivilege ${USER} → ${PERM} failed (HTTP $HTTP)"
        fi
        rm -f /tmp/scw-resp.$$
    done
fi

# ─────────────────────────────────────────────────────────────
section "2. Role identity + PG version"

OWNER_WHO=$(owner -tAc "SELECT current_user")
[ "$OWNER_WHO" = "eurobase_owner" ] && ok "connected as eurobase_owner" || fail "owner DSN authed as $OWNER_WHO"

RO_WHO=$(readonly_ -tAc "SELECT current_user")
[ "$RO_WHO" = "eurobase_readonly" ] && ok "connected as eurobase_readonly" || fail "readonly DSN authed as $RO_WHO — did step 1 grant CONNECT?"

PG_VERSION=$(owner -tAc "SHOW server_version" | tr -d ' ')
info "postgres server version: $PG_VERSION"
case "$PG_VERSION" in
    16*) ok "provisioned on PG 16 (per #389)" ;;
    15*) warn "provisioned on PG 15 — pre-#389 instance" ;;
    *)   warn "unexpected PG version: $PG_VERSION" ;;
esac

# ─────────────────────────────────────────────────────────────
section "3. Tenant schema shape"

TABLE_LIST=$(owner -tAc "SELECT string_agg(tablename, ',' ORDER BY tablename) FROM pg_tables WHERE schemaname='$SCHEMA'")
info "tables in $SCHEMA: $TABLE_LIST"
for expected in email_tokens refresh_tokens storage_objects todos user_identities users vault_secrets; do
    echo ",$TABLE_LIST," | grep -q ",$expected," \
        && ok "  $expected" \
        || fail "missing table: $SCHEMA.$expected"
done

LEAK=$(owner -tAc "
    SELECT count(*) FROM pg_class c
    JOIN pg_namespace n ON n.oid=c.relnamespace
    JOIN pg_roles r ON r.oid=c.relowner
    WHERE n.nspname='$SCHEMA' AND c.relkind='r' AND r.rolname <> 'eurobase_owner'")
[ "$LEAK" = "0" ] && ok "all tenant tables are eurobase_owner-owned (RLS binds for gateway)" \
    || fail "$LEAK tables in $SCHEMA owned by something other than eurobase_owner"

# ─────────────────────────────────────────────────────────────
section "4. RLS boundaries"

COUNT=$(readonly_ -tAc "SELECT count(*) FROM $SCHEMA.todos")
ok "readonly SELECT $SCHEMA.todos → $COUNT rows"

if readonly_ -c "INSERT INTO $SCHEMA.todos (title) VALUES ('rls-fence-test')" 2>&1 | grep -q "permission denied"; then
    ok "readonly INSERT correctly refused (permission denied)"
else
    fail "readonly INSERT succeeded — role is not SELECT-only"
fi

POLICY=$(owner -tAc "SELECT string_agg(polname, ',') FROM pg_policy WHERE polrelid='$SCHEMA.vault_secrets'::regclass")
info "vault_secrets policies: $POLICY"
echo "$POLICY" | grep -q "vault_secrets_policy" && ok "vault_secrets carries vault_secrets_policy" \
    || fail "vault_secrets missing vault_secrets_policy"

# ─────────────────────────────────────────────────────────────
section "5. Owner round-trip on todos (proves DML works via the RW pool)"

INS_ID=$(owner -tAc "INSERT INTO $SCHEMA.todos (title) VALUES ('rw-smoke-'||floor(random()*1e6)) RETURNING id")
[ -n "$INS_ID" ] && ok "INSERT returned id $INS_ID" || fail "INSERT returned nothing"
DEL=$(owner -tAc "DELETE FROM $SCHEMA.todos WHERE id='$INS_ID' RETURNING id")
[ -n "$DEL" ] && ok "DELETE round-tripped" || fail "DELETE returned nothing"

# ─────────────────────────────────────────────────────────────
section "6. SDK smoke (curl → $BASE)"

SFX=$RANDOM
EMAIL="sdk-test-${SFX}@example.com"
PW="sdk-test-pw-${SFX}"

TODOS=$(curl -s -w "\n%{http_code}" "$BASE/v1/data/todos" -H "apikey: $SECRET_KEY")
BODY=$(echo "$TODOS" | head -n -1); CODE=$(echo "$TODOS" | tail -n 1)
[ "$CODE" = "200" ] && ok "GET /v1/data/todos → 200" || fail "GET /v1/data/todos → $CODE  body: $BODY"
ROW_COUNT=$(echo "$BODY" | python3 -c "import sys,json;print(len(json.load(sys.stdin)))" 2>/dev/null || echo "?")
info "  → $ROW_COUNT rows"

NEW=$(curl -s "$BASE/v1/data/todos" -H "apikey: $SECRET_KEY" -H "Content-Type: application/json" \
    -d "{\"title\":\"sdk-smoke-${SFX}\",\"completed\":false}")
NEW_ID=$(echo "$NEW" | python3 -c "import sys,json;print(json.load(sys.stdin).get('id',''))" 2>/dev/null)
[ -n "$NEW_ID" ] && ok "POST /v1/data/todos → id $NEW_ID" || fail "POST /v1/data/todos returned: $NEW"
curl -s -o /dev/null -X DELETE "$BASE/v1/data/todos/$NEW_ID" -H "apikey: $SECRET_KEY"
ok "DELETE /v1/data/todos/$NEW_ID (cleanup)"

VN="sdk_smoke_${SFX}"
V=$(curl -s "$BASE/v1/vault" -H "apikey: $SECRET_KEY" -H "Content-Type: application/json" \
    -d "{\"name\":\"$VN\",\"value\":\"hunter2\",\"description\":\"sdk smoke\"}")
echo "$V" | grep -q '"id"' && ok "POST /v1/vault created secret" || fail "POST /v1/vault: $V"
GET=$(curl -s "$BASE/v1/vault/$VN" -H "apikey: $SECRET_KEY")
echo "$GET" | grep -q '"hunter2"' && ok "GET /v1/vault/$VN decrypted value round-trip" || fail "GET vault: $GET"
curl -s -o /dev/null -X DELETE "$BASE/v1/vault/$VN" -H "apikey: $SECRET_KEY"
ok "DELETE /v1/vault/$VN (cleanup)"

SU=$(curl -s "$BASE/v1/auth/signup" -H "apikey: $PUBLIC_KEY" -H "Content-Type: application/json" \
    -d "{\"email\":\"$EMAIL\",\"password\":\"$PW\"}")
RT=$(echo "$SU" | python3 -c "import sys,json;print(json.load(sys.stdin).get('refresh_token',''))" 2>/dev/null)
[ -n "$RT" ] && ok "POST /v1/auth/signup → refresh_token issued" || fail "signup: $SU"

SI=$(curl -s "$BASE/v1/auth/signin" -H "apikey: $PUBLIC_KEY" -H "Content-Type: application/json" \
    -d "{\"email\":\"$EMAIL\",\"password\":\"$PW\"}")
echo "$SI" | grep -q '"access_token"' && ok "POST /v1/auth/signin → access_token issued" || fail "signin: $SI"

RF=$(curl -s "$BASE/v1/auth/refresh" -H "apikey: $PUBLIC_KEY" -H "Content-Type: application/json" \
    -d "{\"refresh_token\":\"$RT\"}")
echo "$RF" | grep -q '"access_token"' \
    && ok "POST /v1/auth/refresh → new access_token (PR-394 GUC fix works)" \
    || fail "refresh: $RF"

printf "\n${G}✓ ALL CHECKS PASSED${N}\n"
printf "\nSign-off evidence:\n"
printf "  * PG version:      $PG_VERSION\n"
printf "  * Schema:          $SCHEMA\n"
printf "  * Base URL:        $BASE\n"
printf "  * Provider inst.:  $PROVIDER_INSTANCE_ID\n"
printf "  * Timestamp:       %s\n" "$(date -u +%FT%TZ)"
