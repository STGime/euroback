#!/usr/bin/env bash
# Reproduces the tenant-migration isolation verification for #190 / PR #209
# against a throwaway Postgres, with NO app dependencies. Builds the minimal
# role topology (the shape migration 000063 installs), then runs the attack
# battery a malicious migration body could attempt. Every attack must be
# DENIED; the legitimate own-schema DDL must succeed.
#
# Usage:  ./scripts/verify-tenant-migration-isolation.sh
# Requires: docker.
set -euo pipefail

CNAME=eb-mig-isolation
docker rm -f "$CNAME" >/dev/null 2>&1 || true
docker run -d --name "$CNAME" -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=eurobase -p 5456:5432 postgres:16-alpine >/dev/null
trap 'docker rm -f "$CNAME" >/dev/null 2>&1 || true' EXIT
for _ in $(seq 1 30); do docker exec "$CNAME" pg_isready -U postgres -d eurobase >/dev/null 2>&1 && break; sleep 1; done

psql() { docker exec -i "$CNAME" psql -U postgres -d eurobase "$@"; }
as_role() { local role="$1" pw="$2"; shift 2; PGPASSWORD="$pw" docker exec -i "$CNAME" psql -h localhost -U "$role" -d eurobase "$@"; }

psql -v ON_ERROR_STOP=1 >/dev/null <<'SQL'
CREATE EXTENSION IF NOT EXISTS "pgcrypto";
CREATE ROLE eurobase_migrator CREATEROLE;
CREATE ROLE eurobase_gateway LOGIN PASSWORD 'pw';
CREATE TABLE public.projects (id uuid primary key, schema_name text, plan text default 'free');
CREATE TABLE public.tenant_migrations (id uuid primary key default gen_random_uuid(), project_id uuid, version bigint, name text default '', sql text, checksum text, applied_by text, applied_at timestamptz default now(), unique(project_id, version));
ALTER TABLE public.projects OWNER TO eurobase_migrator;
ALTER TABLE public.tenant_migrations OWNER TO eurobase_migrator;
GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA public TO eurobase_gateway;
INSERT INTO public.projects VALUES ('11111111-1111-1111-1111-111111111111','tenant_a','free'),('22222222-2222-2222-2222-222222222222','tenant_b','free');
CREATE SCHEMA tenant_a AUTHORIZATION eurobase_migrator;
CREATE SCHEMA tenant_b AUTHORIZATION eurobase_migrator;
CREATE TABLE tenant_b.secrets (id int primary key, val text); INSERT INTO tenant_b.secrets VALUES (1,'tenant-b-private');
ALTER TABLE tenant_b.secrets OWNER TO eurobase_migrator;

-- session_user-bound bookkeeping helper (000063).
CREATE FUNCTION public.record_tenant_migration(v bigint, n text, s text, c text) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path=public,pg_temp AS $$
DECLARE sch text; proj uuid; BEGIN
  IF right(session_user,4)<>'_ddl' THEN RAISE EXCEPTION 'not a ddl role'; END IF;
  sch := left(session_user, length(session_user)-4);
  SELECT id INTO proj FROM public.projects WHERE schema_name=sch;
  INSERT INTO public.tenant_migrations(project_id,version,name,sql,checksum,applied_by) VALUES (proj,v,n,s,c,session_user);
END$$;
ALTER FUNCTION public.record_tenant_migration(bigint,text,text,text) OWNER TO eurobase_migrator;
REVOKE ALL ON FUNCTION public.record_tenant_migration(bigint,text,text,text) FROM PUBLIC;

-- per-tenant LOGIN ddl roles, member of NOTHING (the key property).
CREATE ROLE tenant_a_ddl LOGIN PASSWORD 'pw_a';
CREATE ROLE tenant_b_ddl LOGIN PASSWORD 'pw_b';
GRANT USAGE,CREATE ON SCHEMA tenant_a TO tenant_a_ddl;
GRANT USAGE,CREATE ON SCHEMA tenant_b TO tenant_b_ddl;
GRANT tenant_a_ddl TO eurobase_migrator; GRANT tenant_b_ddl TO eurobase_migrator;
ALTER TABLE tenant_b.secrets OWNER TO tenant_b_ddl;
GRANT EXECUTE ON FUNCTION public.record_tenant_migration(bigint,text,text,text) TO tenant_a_ddl, tenant_b_ddl;
SQL

fail() { echo "FAIL: $1"; exit 1; }
# Capture (no pipe) so grep -q + pipefail can't SIGPIPE the upstream.
must_deny() { local label="$1"; shift; local out; out="$("$@" 2>&1 || true)"; \
  echo "$out" | grep -qi "permission denied\|must be" || fail "expected denial ($label); got: $out"; }

echo "1. cross-tenant pivot via RESET ROLE ..."
must_deny pivot as_role tenant_a_ddl pw_a -tAc "DO \$\$ BEGIN EXECUTE 'RESET ROLE'; EXECUTE 'SET ROLE tenant_b_ddl'; EXECUTE 'DROP TABLE tenant_b.secrets'; END \$\$;"
echo "2. platform table write ..."
must_deny "platform write" as_role tenant_a_ddl pw_a -tAc "UPDATE public.projects SET plan='pro';"
echo "3. direct bookkeeping forge ..."
must_deny forge as_role tenant_a_ddl pw_a -tAc "INSERT INTO public.tenant_migrations(project_id,version,sql,checksum) VALUES ('22222222-2222-2222-2222-222222222222',9,'x','y');"
echo "4. cross-tenant table read ..."
must_deny "cross read" as_role tenant_a_ddl pw_a -tAc "SELECT * FROM tenant_b.secrets;"
echo "5. tenant_b.secrets intact ..."
[ "$(psql -tAc 'SELECT count(*) FROM tenant_b.secrets;')" = "1" ] || fail "tenant_b.secrets was modified"
echo "6. legitimate own-schema DDL succeeds ..."
as_role tenant_a_ddl pw_a -v ON_ERROR_STOP=1 -tAc "SET search_path TO tenant_a; CREATE TABLE t (id uuid PRIMARY KEY DEFAULT gen_random_uuid(), s text, UNIQUE(s)); CREATE INDEX ti ON t(s) WHERE s IS NOT NULL;" >/dev/null || fail "own-schema DDL rejected"
echo "7. bookkeeping bound to session_user (forgery-proof) ..."
as_role tenant_a_ddl pw_a -tAc "SELECT public.record_tenant_migration(1,'init','s','c');" >/dev/null
got=$(psql -tAc "SELECT project_id FROM public.tenant_migrations WHERE applied_by='tenant_a_ddl';")
[ "$got" = "11111111-1111-1111-1111-111111111111" ] || fail "bookkeeping not bound to session_user (got $got)"

echo
echo "ALL CHECKS PASSED — tenant migrations are isolated to one tenant."
