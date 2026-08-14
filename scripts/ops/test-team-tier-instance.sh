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
# Secrets must be supplied via env (see the `:?` guards below);
# non-secret identifiers (PROJECT_ID / PROVIDER_INSTANCE_ID /
# SCW_REGION) default to myteam3 for a quick check but the four
# credentials + SLUG have no defaults — never re-add them to this
# file, this repo is public (see feedback memory
# no_secrets_in_scripts.md; PR #395 review).

set -uo pipefail

# All credentials are REQUIRED and must come from the environment
# — never hardcode them here. This repo is public; a previous
# revision of this file shipped live myteam3 credentials as
# defaults and they had to be rotated (see PR #395 review).
# `: "${VAR:?…}"` errors with a helpful message if VAR is unset.
: "${OWNER_DSN:?OWNER_DSN required: postgres://eurobase_owner:PW@host:port/rdb?sslmode=require}"
: "${READONLY_DSN:?READONLY_DSN required: postgres://eurobase_readonly:PW@host:port/rdb?sslmode=require}"
: "${SLUG:?SLUG required (used to build the SDK base URL: https://\$SLUG.eurobase.app)}"
: "${PUBLIC_KEY:?PUBLIC_KEY required (eb_pk_…) — reveal via the console API tab}"
: "${SECRET_KEY:?SECRET_KEY required (eb_sk_…) — reveal via the console API tab}"

# Non-secret identifiers — safe as defaults for a myteam3-focused
# quick check; override for other projects. Also OK to leave here
# because publishing an instance UUID or project UUID by itself
# grants nothing.
: "${PROJECT_ID:=19fac72b-68a1-4e88-9ea2-9fd68a94e200}"
: "${PROVIDER_INSTANCE_ID:=4aa613f3-e354-47eb-8da4-cec3765716df}"
: "${SCW_REGION:=fr-par}"

# Scaleway API secret — only needed for step 1 (privilege grant).
# When unset, step 1 skips with an instruction on how to pull it
# from the k8s Secret.
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

    # Scaleway's `permission=readonly` grants MORE than SELECT
    # (verified — the role could INSERT into tenant tables). Lock
    # it down to SELECT-only via SQL, running as the DB owner.
    # Same statements as dbprovider.LockdownReadonlyGrants — kept
    # in sync manually (worth a follow-up to export the SQL for
    # reuse from the script).
    owner -tAc "
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON ALL TABLES IN SCHEMA \"$SCHEMA\" FROM eurobase_readonly;
        REVOKE UPDATE ON ALL SEQUENCES IN SCHEMA \"$SCHEMA\" FROM eurobase_readonly;
        ALTER DEFAULT PRIVILEGES FOR ROLE eurobase_owner IN SCHEMA \"$SCHEMA\" REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLES FROM eurobase_readonly;
        ALTER DEFAULT PRIVILEGES FOR ROLE eurobase_owner IN SCHEMA \"$SCHEMA\" REVOKE UPDATE ON SEQUENCES FROM eurobase_readonly;
        GRANT SELECT ON ALL TABLES IN SCHEMA \"$SCHEMA\" TO eurobase_readonly;
        GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA \"$SCHEMA\" TO eurobase_readonly;
    " >/dev/null && ok "readonly locked down to SELECT-only on $SCHEMA" \
        || fail "readonly lockdown SQL failed"
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

# `|| true` swallows psql's non-zero exit (expected on permission
# denied) so `set -o pipefail` at the top doesn't propagate it and
# collapse the whole `if` into the else branch — that was the
# false-positive "readonly INSERT succeeded" bug the diagnostics
# below caught (has_table_privilege said INSERT=f while this test
# claimed the INSERT worked).
INSERT_OUT=$(readonly_ -c "INSERT INTO $SCHEMA.todos (title) VALUES ('rls-fence-test')" 2>&1 || true)
if echo "$INSERT_OUT" | grep -q "permission denied"; then
    ok "readonly INSERT correctly refused (permission denied)"
else
    warn "readonly INSERT succeeded (or failed with a non-permission error) — diagnosing…"
    info "raw psql output: $INSERT_OUT"
    info "--- role memberships of eurobase_readonly ---"
    owner -c "SELECT r.rolname AS parent_role FROM pg_roles r JOIN pg_auth_members m ON m.roleid=r.oid JOIN pg_roles c ON c.oid=m.member WHERE c.rolname='eurobase_readonly'"
    info "--- grants on $SCHEMA.todos ---"
    owner -c "SELECT grantee, privilege_type FROM information_schema.table_privileges WHERE table_schema='$SCHEMA' AND table_name='todos' ORDER BY grantee, privilege_type"
    info "--- has_table_privilege probes ---"
    owner -c "SELECT has_table_privilege('eurobase_readonly', '$SCHEMA.todos', 'SELECT') AS sel, has_table_privilege('eurobase_readonly', '$SCHEMA.todos', 'INSERT') AS ins, has_table_privilege('eurobase_readonly', '$SCHEMA.todos', 'UPDATE') AS upd, has_table_privilege('eurobase_readonly', '$SCHEMA.todos', 'DELETE') AS del"
    info "--- schema-level ACL ---"
    owner -c "SELECT nspname, nspacl FROM pg_namespace WHERE nspname='$SCHEMA'"
    info "--- table ACL raw ---"
    owner -c "SELECT relname, relacl FROM pg_class WHERE relnamespace=(SELECT oid FROM pg_namespace WHERE nspname='$SCHEMA') AND relname='todos'"
    info "--- default privileges configured ---"
    owner -c "SELECT defaclrole::regrole AS owner_role, defaclnamespace::regnamespace AS schema, defaclobjtype, defaclacl FROM pg_default_acl WHERE defaclnamespace=(SELECT oid FROM pg_namespace WHERE nspname='$SCHEMA')"
    fail "readonly is not SELECT-only — see diagnostics above"
fi

POLICY=$(owner -tAc "SELECT string_agg(polname, ',') FROM pg_policy WHERE polrelid='$SCHEMA.vault_secrets'::regclass")
info "vault_secrets policies: $POLICY"
echo "$POLICY" | grep -q "vault_secrets_policy" && ok "vault_secrets carries vault_secrets_policy" \
    || fail "vault_secrets missing vault_secrets_policy"

# ─────────────────────────────────────────────────────────────
section "5. Owner round-trip on todos (proves DML works via the RW pool)"

# `head -1` strips psql's "INSERT 0 1"/"DELETE 1" footer that
# leaks through even in -tA mode for DML-with-RETURNING (which
# behaves differently from a plain SELECT there — Postgres emits
# the command tag alongside the RETURNING tuple).
INS_ID=$(owner -tAc "INSERT INTO $SCHEMA.todos (title) VALUES ('rw-smoke-'||floor(random()*1e6)) RETURNING id" | head -1)
[ -n "$INS_ID" ] && ok "INSERT returned id $INS_ID" || fail "INSERT returned nothing"
DEL=$(owner -tAc "DELETE FROM $SCHEMA.todos WHERE id='$INS_ID' RETURNING id" | head -1)
[ -n "$DEL" ] && ok "DELETE round-tripped" || fail "DELETE returned nothing"

# ─────────────────────────────────────────────────────────────
section "6. SDK smoke (curl → $BASE)"

SFX=$RANDOM
EMAIL="sdk-test-${SFX}@example.com"
PW="sdk-test-pw-${SFX}"

TODOS=$(curl -s -w "\n%{http_code}" "$BASE/v1/db/todos" -H "apikey: $SECRET_KEY")
# BSD head (macOS) doesn't support `head -n -1`; sed '$d' deletes
# the last line (portable across GNU + BSD).
BODY=$(echo "$TODOS" | sed '$d'); CODE=$(echo "$TODOS" | tail -n 1)
if [ "$CODE" != "200" ]; then
    info "--- diagnostic curl -v for the 404 ---"
    curl -sv "$BASE/v1/db/todos" -H "apikey: $SECRET_KEY" 2>&1 | grep -E "^(> |< |\*)" | head -20
    fail "GET /v1/db/todos → $CODE  body: $BODY"
fi
ok "GET /v1/db/todos → 200"
ROW_COUNT=$(echo "$BODY" | python3 -c "import sys,json;print(len(json.load(sys.stdin)))" 2>/dev/null || echo "?")
info "  → $ROW_COUNT rows"

NEW=$(curl -s "$BASE/v1/db/todos" -H "apikey: $SECRET_KEY" -H "Content-Type: application/json" \
    -d "{\"title\":\"sdk-smoke-${SFX}\",\"completed\":false}")
NEW_ID=$(echo "$NEW" | python3 -c "import sys,json;print(json.load(sys.stdin).get('id',''))" 2>/dev/null)
[ -n "$NEW_ID" ] && ok "POST /v1/db/todos → id $NEW_ID" || fail "POST /v1/db/todos returned: $NEW"
curl -s -o /dev/null -X DELETE "$BASE/v1/db/todos/$NEW_ID" -H "apikey: $SECRET_KEY"
ok "DELETE /v1/db/todos/$NEW_ID (cleanup)"

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
