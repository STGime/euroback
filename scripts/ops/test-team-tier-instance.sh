#!/usr/bin/env bash
# test-team-tier-instance.sh
#
# End-to-end validation for a Team-tier project's dedicated Scaleway
# instance + the SDK surface that sits on top of it.
#
# Does two orthogonal things:
#
#   1. **Backfill grant.** Applies `GRANT CONNECT ON DATABASE …
#      TO eurobase_gateway, eurobase_readonly` to catch any
#      instance provisioned before dedicated_bootstrap.sql's DB-
#      CONNECT block landed (bootstrap is idempotent; a re-bootstrap
#      would grant it too, but ops doesn't want to force a re-
#      bootstrap for a working instance just for this).
#
#   2. **Direct DB smoke.** Connects as eurobase_readonly and
#      eurobase_owner, verifies role privileges + tenant schema
#      shape + RLS boundaries + vault policies.
#
#   3. **SDK smoke.** curl against the project subdomain: data CRUD,
#      vault round-trip, end-user signup / signin / refresh — the
#      full A→D + #391 + #394 path.
#
# Configure via env — no secrets in argv (they'd leak into ps):
#
#   OWNER_DSN     — postgres://eurobase_owner:PW@host:port/rdb?sslmode=require
#   READONLY_DSN  — postgres://eurobase_readonly:PW@host:port/rdb?sslmode=require
#   PROJECT_ID    — the UUID (used to derive tenant_<id> schema name)
#   SLUG          — project slug (for the SDK base URL: https://<slug>.eurobase.app)
#   PUBLIC_KEY    — eb_pk_…  (SDK auth for anonymous / signup)
#   SECRET_KEY    — eb_sk_…  (SDK auth for service-role writes)
#
# Everything defaults to the myteam3 values for a quick reproducible
# check; override any one to point at a different Team-tier project.
#
# Exits non-zero on the first failure. On success, prints a summary
# suitable for pasting into a PR body as sign-off evidence.

set -uo pipefail

: "${OWNER_DSN:=postgres://eurobase_owner:-S.YwTkXSBKTTNB%3EtpzxFKx~%3E;D4;KkR@51.159.205.20:27697/rdb?sslmode=require}"
: "${READONLY_DSN:=postgres://eurobase_readonly:b666a78b4acd546efcb23c61b322cbf34b8e39a361078e4769230cb35aebd0f5@51.159.205.20:27697/rdb?sslmode=require}"
: "${PROJECT_ID:=19fac72b-68a1-4e88-9ea2-9fd68a94e200}"
: "${SLUG:=myteam3}"
: "${PUBLIC_KEY:=eb_pk_a5eecb0e86cd816d4f51ec1c8b1c2438}"
: "${SECRET_KEY:=eb_sk_25eecf6cf8c47f4d9638afce036f0ec6}"

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

# Colour when TTY
if [ -t 1 ]; then
    G=$'\033[32m'; R=$'\033[31m'; Y=$'\033[33m'; B=$'\033[1;36m'; N=$'\033[0m'
else G=""; R=""; Y=""; B=""; N=""; fi

section() { printf "\n${B}══ %s ══${N}\n" "$1"; }
ok()      { printf "  ${G}✓${N} %s\n" "$1"; }
warn()    { printf "  ${Y}⚠${N} %s\n" "$1"; }
fail()    { printf "  ${R}✗ FAIL:${N} %s\n" "$1"; exit 1; }
info()    { printf "    %s\n" "$1"; }

owner()   { "$PSQL" "$OWNER_DSN"    "$@"; }
readonly_() { "$PSQL" "$READONLY_DSN" "$@"; }

trap 'echo; echo "aborted at line $LINENO"; exit 1' ERR

# ─────────────────────────────────────────────────────────────
section "1. Backfill CONNECT grants (fixes pre-bootstrap-grant instances)"

owner -tAc "GRANT CONNECT ON DATABASE rdb TO eurobase_gateway, eurobase_readonly" >/dev/null \
    && ok "GRANT CONNECT applied to eurobase_gateway + eurobase_readonly" \
    || fail "GRANT CONNECT failed — check OWNER_DSN"

# ─────────────────────────────────────────────────────────────
section "2. Role identity + PG version"

OWNER_WHO=$(owner -tAc "SELECT current_user")
[ "$OWNER_WHO" = "eurobase_owner" ] && ok "connected as eurobase_owner" || fail "owner DSN authed as $OWNER_WHO"

RO_WHO=$(readonly_ -tAc "SELECT current_user")
[ "$RO_WHO" = "eurobase_readonly" ] && ok "connected as eurobase_readonly" || fail "readonly DSN authed as $RO_WHO"

PG_VERSION=$(owner -tAc "SHOW server_version" | tr -d ' ')
info "postgres server version: $PG_VERSION"
case "$PG_VERSION" in
    16*) ok "provisioned on PG 16 (per issue #382 fix)" ;;
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

# All tables owner-owned so RLS binds for eurobase_gateway
LEAK=$(owner -tAc "
    SELECT count(*) FROM pg_class c
    JOIN pg_namespace n ON n.oid=c.relnamespace
    JOIN pg_roles r ON r.oid=c.relowner
    WHERE n.nspname='$SCHEMA' AND c.relkind='r' AND r.rolname <> 'eurobase_owner'")
[ "$LEAK" = "0" ] && ok "all tenant tables are eurobase_owner-owned (RLS binds for gateway)" \
    || fail "$LEAK tables in $SCHEMA owned by something other than eurobase_owner"

# ─────────────────────────────────────────────────────────────
section "4. RLS boundaries"

# readonly can SELECT
COUNT=$(readonly_ -tAc "SELECT count(*) FROM $SCHEMA.todos")
ok "readonly SELECT $SCHEMA.todos → $COUNT rows"

# readonly CANNOT INSERT (SELECT-only role)
if readonly_ -c "INSERT INTO $SCHEMA.todos (title) VALUES ('rls-fence-test')" 2>&1 | grep -q "permission denied"; then
    ok "readonly INSERT correctly refused (permission denied)"
else
    fail "readonly INSERT succeeded — role is not SELECT-only"
fi

# vault_secrets has the internal_auth_path policy
POLICY=$(owner -tAc "SELECT polname FROM pg_policy WHERE polrelid='$SCHEMA.vault_secrets'::regclass" | tr '\n' ',')
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

# GET todos as secret key
TODOS=$(curl -s -w "\n%{http_code}" "$BASE/v1/data/todos" -H "apikey: $SECRET_KEY")
BODY=$(echo "$TODOS" | head -n -1); CODE=$(echo "$TODOS" | tail -n 1)
[ "$CODE" = "200" ] && ok "GET /v1/data/todos → 200" || fail "GET /v1/data/todos → $CODE  body: $BODY"
ROW_COUNT=$(echo "$BODY" | python3 -c "import sys,json;print(len(json.load(sys.stdin)))" 2>/dev/null || echo "?")
info "  → $ROW_COUNT rows"

# INSERT + verify + cleanup
NEW=$(curl -s "$BASE/v1/data/todos" -H "apikey: $SECRET_KEY" -H "Content-Type: application/json" \
    -d "{\"title\":\"sdk-smoke-${SFX}\",\"completed\":false}")
NEW_ID=$(echo "$NEW" | python3 -c "import sys,json;print(json.load(sys.stdin).get('id',''))" 2>/dev/null)
[ -n "$NEW_ID" ] && ok "POST /v1/data/todos → id $NEW_ID" || fail "POST /v1/data/todos returned: $NEW"
curl -s -o /dev/null -X DELETE "$BASE/v1/data/todos/$NEW_ID" -H "apikey: $SECRET_KEY"
ok "DELETE /v1/data/todos/$NEW_ID (cleanup)"

# Vault round-trip
VN="sdk_smoke_${SFX}"
V=$(curl -s "$BASE/v1/vault" -H "apikey: $SECRET_KEY" -H "Content-Type: application/json" \
    -d "{\"name\":\"$VN\",\"value\":\"hunter2\",\"description\":\"sdk smoke\"}")
echo "$V" | grep -q '"id"' && ok "POST /v1/vault created secret" || fail "POST /v1/vault: $V"
GET=$(curl -s "$BASE/v1/vault/$VN" -H "apikey: $SECRET_KEY")
echo "$GET" | grep -q '"hunter2"' && ok "GET /v1/vault/$VN decrypted value round-trip" || fail "GET vault: $GET"
curl -s -o /dev/null -X DELETE "$BASE/v1/vault/$VN" -H "apikey: $SECRET_KEY"
ok "DELETE /v1/vault/$VN (cleanup)"

# Auth signup / signin / refresh
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

# ─────────────────────────────────────────────────────────────
printf "\n${G}✓ ALL CHECKS PASSED${N}\n"
printf "\nSign-off evidence:\n"
printf "  * PG version:  $PG_VERSION\n"
printf "  * Schema:      $SCHEMA\n"
printf "  * Base URL:    $BASE\n"
printf "  * Tables:      %s\n" "$(echo "$TABLE_LIST" | tr ',' ' ')"
printf "  * Timestamp:   %s\n" "$(date -u +%FT%TZ)"
