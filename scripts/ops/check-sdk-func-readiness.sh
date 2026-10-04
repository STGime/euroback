#!/usr/bin/env bash
# Read-only pre-flip gate for SDK_FUNC_LOGIN (step 4): before SDK customer
# SQL (/v1/db/sql + RPC) moves onto per-project `<schema>_func` logins, every
# tenant schema must satisfy two invariants, or the flip changes behaviour:
#
#   1. No application table is still owned by eurobase_gateway. The gateway
#      is the owner of such a table, so RLS is BYPASSED for SDK traffic
#      today; running as `_func` (a non-owner) would start enforcing it.
#   2. `_func` has full DML (SELECT/INSERT/UPDATE/DELETE) on every
#      application table, or customer SQL fails with permission denied.
#
# "Application table" = relkind 'r'/'p' excluding the platform-managed system
# tables — identical to migration 000136's converge_tenant_ownership and
# internal/query/func_readiness.go (keep the three in sync). Prints every
# offending (schema, table) and exits non-zero if any exist (target: 0).
#
# Runs a one-off Job in-cluster (the prod DB is only reachable there) as the
# developer role, same pattern as scripts/ops/check-migration-bootstrap.sh.
#
# Usage: ./scripts/ops/check-sdk-func-readiness.sh
set -euo pipefail
NS=eurobase
JOB=sdk-func-readiness-check
IMG=rg.fr-par.scw.cloud/eurobase-app/migrations:latest

kubectl -n "$NS" delete job "$JOB" --ignore-not-found --wait=true >/dev/null 2>&1 || true
kubectl -n "$NS" apply -f - >/dev/null <<'YAML'
apiVersion: batch/v1
kind: Job
metadata: { name: sdk-func-readiness-check, namespace: eurobase }
spec:
  backoffLimit: 0
  ttlSecondsAfterFinished: 200
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: c
          image: rg.fr-par.scw.cloud/eurobase-app/migrations:latest
          imagePullPolicy: Always
          command: ["sh", "-c", 'printf "%s\n" "$CHECK_SQL" | psql "$DATABASE_URL_DEVELOPER" -tA']
          env:
            - name: DATABASE_URL_DEVELOPER
              valueFrom: { secretKeyRef: { name: eurobase-secrets, key: DATABASE_URL_DEVELOPER } }
            # Two self-contained statements (a CTE would only scope the
            # first). "app" = application tables of every provisioned tenant
            # schema whose _func role exists, excluding system tables — the
            # same set as internal/query/func_readiness.go and migration
            # 000136.
            - name: CHECK_SQL
              value: |
                SELECT 'GATEWAY_OWNED ' || n.nspname || '.' || c.relname ||
                       (CASE WHEN c.relrowsecurity THEN ' (RLS enabled)' ELSE '' END)
                  FROM pg_class c
                  JOIN pg_namespace n ON n.oid = c.relnamespace
                  JOIN pg_roles r ON r.oid = c.relowner
                 WHERE n.nspname IN (SELECT schema_name FROM public.projects WHERE schema_name IS NOT NULL)
                   AND c.relkind IN ('r','p')
                   AND r.rolname = 'eurobase_gateway'
                   AND c.relname <> ALL (ARRAY['users','user_identities','refresh_tokens',
                       'email_tokens','storage_objects','storage_shared_prefixes','vault_secrets'])
                 ORDER BY 1;
                -- A provisioned schema with no _func role would 503 at connect
                -- after the flip — surface it rather than silently skip it.
                SELECT 'MISSING_FUNC_ROLE ' || schema_name
                  FROM public.projects p
                 WHERE schema_name IS NOT NULL
                   AND EXISTS (SELECT 1 FROM pg_namespace n WHERE n.nspname = p.schema_name)
                   AND NOT EXISTS (SELECT 1 FROM pg_roles r WHERE r.rolname = p.schema_name || '_func')
                 ORDER BY 1;
                SELECT 'FUNC_MISSING_DML ' || n.nspname || '.' || c.relname
                  FROM pg_class c
                  JOIN pg_namespace n ON n.oid = c.relnamespace
                  JOIN pg_roles fr ON fr.rolname = n.nspname || '_func'
                 WHERE n.nspname IN (SELECT schema_name FROM public.projects WHERE schema_name IS NOT NULL)
                   AND c.relkind IN ('r','p')
                   AND c.relname <> ALL (ARRAY['users','user_identities','refresh_tokens',
                       'email_tokens','storage_objects','storage_shared_prefixes','vault_secrets'])
                   AND NOT (
                       has_table_privilege(fr.oid, c.oid, 'SELECT')
                   AND has_table_privilege(fr.oid, c.oid, 'INSERT')
                   AND has_table_privilege(fr.oid, c.oid, 'UPDATE')
                   AND has_table_privilege(fr.oid, c.oid, 'DELETE'))
                 ORDER BY 1;
YAML

kubectl -n "$NS" wait --for=condition=complete --timeout=120s "job/$JOB" >/dev/null 2>&1 || true
OUT=$(kubectl -n "$NS" logs "job/$JOB" 2>&1 || true)
kubectl -n "$NS" delete job "$JOB" --ignore-not-found >/dev/null 2>&1 || true

OFFENDERS=$(printf "%s\n" "$OUT" | grep -E '^(GATEWAY_OWNED|FUNC_MISSING_DML|MISSING_FUNC_ROLE) ' || true)
if [ -n "$OFFENDERS" ]; then
  echo "NOT READY for SDK_FUNC_LOGIN — fix these first:" >&2
  printf "%s\n" "$OFFENDERS" >&2
  echo "" >&2
  echo "  GATEWAY_OWNED     → reassign the table to <schema>_ddl (settle its service policy first)." >&2
  echo "  FUNC_MISSING_DML  → GRANT SELECT,INSERT,UPDATE,DELETE to <schema>_func (re-run migration 000136's converge)." >&2
  echo "  MISSING_FUNC_ROLE → the schema has no <schema>_func role; the worker's Ensurer should create it (provision_tenant)." >&2
  exit 1
fi

echo "READY: no gateway-owned application tables, and <schema>_func has full DML on every application table."
