#!/usr/bin/env bash
# Reproduces the billing_profiles PII isolation control from PR #443
# against a throwaway Postgres. Every access from eurobase_gateway
# (the SDK runtime role) must be DENIED after migration 000106;
# eurobase_developer must retain full DML.
#
# The trap: migration 000037 sets ALTER DEFAULT PRIVILEGES for
# migrator-in-schema-public that auto-grants DML on new tables to
# eurobase_gateway. So a plain "GRANT ... TO developer; skip
# gateway" does NOT withhold anything — the default privilege has
# already fired. Migration 000106 must explicitly REVOKE the
# billing_profiles table from gateway.
#
# Usage:  ./scripts/verify-billing-profile-isolation.sh
# Requires: docker.
set -euo pipefail

CNAME=eb-billing-profile-isolation
docker rm -f "$CNAME" >/dev/null 2>&1 || true
docker run -d --name "$CNAME" \
    -e POSTGRES_PASSWORD=postgres \
    -e POSTGRES_DB=eurobase \
    -p 5457:5432 \
    postgres:16-alpine >/dev/null
trap 'docker rm -f "$CNAME" >/dev/null 2>&1 || true' EXIT

for _ in $(seq 1 30); do
    docker exec "$CNAME" pg_isready -U postgres -d eurobase >/dev/null 2>&1 && break
    sleep 1
done

psql() { docker exec -i "$CNAME" psql -U postgres -d eurobase "$@"; }

# ── Set up prod-like role topology ──────────────────────────────
psql -v ON_ERROR_STOP=1 >/dev/null <<'SQL'
CREATE EXTENSION IF NOT EXISTS "pgcrypto";
REVOKE CONNECT ON DATABASE eurobase FROM PUBLIC;

-- Match production: migrator is NOINHERIT + CREATEROLE (mirrors
-- Scaleway's real shape per CLAUDE.md § Postgres roles).
CREATE ROLE eurobase_migrator CREATEROLE NOINHERIT;
CREATE ROLE eurobase_gateway   LOGIN PASSWORD 'pw';
CREATE ROLE eurobase_developer LOGIN PASSWORD 'pw' IN ROLE eurobase_migrator INHERIT;

GRANT CONNECT ON DATABASE eurobase TO eurobase_gateway;
GRANT CONNECT ON DATABASE eurobase TO eurobase_developer;
GRANT USAGE ON SCHEMA public TO eurobase_gateway;
GRANT USAGE ON SCHEMA public TO eurobase_developer;

-- The rest of the platform_users shape only matters for the FK.
CREATE TABLE public.platform_users (id uuid primary key);
ALTER TABLE public.platform_users OWNER TO eurobase_migrator;

-- Replay migration 000037's default-privileges grant (the load-
-- bearing hostile default the review flagged).
ALTER DEFAULT PRIVILEGES FOR ROLE eurobase_migrator IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO eurobase_gateway;
SQL

# ── Apply migration 000106 (the actual PR file) ─────────────────
# Migrator must own new objects, so we SET ROLE first.
docker cp /Users/stefangimeson/euroback/migrations/000106_billing_profiles.up.sql \
    "$CNAME:/tmp/000106.sql" >/dev/null
psql -v ON_ERROR_STOP=1 >/dev/null <<'SQL'
SET ROLE eurobase_migrator;
\i /tmp/000106.sql
RESET ROLE;
SQL

echo "── Check 1: eurobase_developer retains full DML ──"
psql -v ON_ERROR_STOP=1 -c "
SELECT
    has_table_privilege('eurobase_developer','public.billing_profiles','SELECT') AS dev_select,
    has_table_privilege('eurobase_developer','public.billing_profiles','INSERT') AS dev_insert,
    has_table_privilege('eurobase_developer','public.billing_profiles','UPDATE') AS dev_update,
    has_table_privilege('eurobase_developer','public.billing_profiles','DELETE') AS dev_delete;
"

DEV_ROW=$(psql -tA -c "SELECT
    has_table_privilege('eurobase_developer','public.billing_profiles','SELECT')::text ||
    has_table_privilege('eurobase_developer','public.billing_profiles','INSERT')::text ||
    has_table_privilege('eurobase_developer','public.billing_profiles','UPDATE')::text ||
    has_table_privilege('eurobase_developer','public.billing_profiles','DELETE')::text;
")
if [[ "$DEV_ROW" != "truetruetruetrue" ]]; then
    echo "FAIL: eurobase_developer lost expected privileges (got $DEV_ROW)"
    exit 1
fi
echo "PASS: developer has SELECT/INSERT/UPDATE/DELETE"

echo
echo "── Check 2: eurobase_gateway has NO privilege on billing_profiles ──"
psql -v ON_ERROR_STOP=1 -c "
SELECT
    has_table_privilege('eurobase_gateway','public.billing_profiles','SELECT') AS gw_select,
    has_table_privilege('eurobase_gateway','public.billing_profiles','INSERT') AS gw_insert,
    has_table_privilege('eurobase_gateway','public.billing_profiles','UPDATE') AS gw_update,
    has_table_privilege('eurobase_gateway','public.billing_profiles','DELETE') AS gw_delete;
"

GW_ROW=$(psql -tA -c "SELECT
    has_table_privilege('eurobase_gateway','public.billing_profiles','SELECT')::text ||
    has_table_privilege('eurobase_gateway','public.billing_profiles','INSERT')::text ||
    has_table_privilege('eurobase_gateway','public.billing_profiles','UPDATE')::text ||
    has_table_privilege('eurobase_gateway','public.billing_profiles','DELETE')::text;
")
if [[ "$GW_ROW" != "falsefalsefalsefalse" ]]; then
    echo "FAIL: eurobase_gateway can access billing_profiles (got $GW_ROW)"
    echo "     — the REVOKE in migration 000106 is missing or ineffective."
    exit 1
fi
echo "PASS: gateway has zero access — the REVOKE holds against 000037's default grant"

echo
echo "── Check 3: end-to-end DML from each role ──"
psql -v ON_ERROR_STOP=1 >/dev/null <<'SQL'
INSERT INTO public.platform_users (id) VALUES
    ('11111111-1111-1111-1111-111111111111');
SQL

# developer should succeed
PGPASSWORD=pw docker exec -i "$CNAME" psql -h localhost -U eurobase_developer -d eurobase -v ON_ERROR_STOP=1 -c "
INSERT INTO public.billing_profiles
    (platform_user_id, entity_type, legal_name, street_address, postal_code, city, country)
VALUES
    ('11111111-1111-1111-1111-111111111111','business','Example OÜ','Ahtri 12','15551','Tallinn','EE');
" >/dev/null
echo "PASS: developer INSERT succeeded"

# gateway should fail with 42501
if PGPASSWORD=pw docker exec -i "$CNAME" psql -h localhost -U eurobase_gateway -d eurobase -v ON_ERROR_STOP=1 -c "
SELECT legal_name FROM public.billing_profiles LIMIT 1;
" >/dev/null 2>&1; then
    echo "FAIL: eurobase_gateway can SELECT billing_profiles"
    exit 1
fi
echo "PASS: gateway SELECT denied (permission denied for table billing_profiles)"

echo
echo "── All checks passed. Migration 000106 isolation is real. ──"
