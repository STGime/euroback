#!/usr/bin/env bash
# Read-only pre-flip inventory for PLATFORM_DDL_LOGIN (step 6c/6e): before
# console SQL / MCP SQL / schema DDL move onto per-project `<schema>_ddl`
# logins, report everything a tenant schema holds that is NOT owned by its
# own `_ddl` role (so running as `_ddl` would hit permission denied or a
# behaviour change), plus roles that aren't ready.
#
# It reports, per provisioned tenant schema:
#   MIGRATOR_OBJECT  — a table/sequence/view/function/type owned by
#                      eurobase_migrator. These are migrator-owned because the
#                      console SQL / migrations path creates objects as
#                      migrator. The 6c convergence migration reassigns them to
#                      `_ddl`. SECURITY DEFINER functions among these change who
#                      they run as once reassigned (migrator → _ddl) — review.
#   FOREIGN_OBJECT   — owned by eurobase_developer or eurobase_gateway: needs a
#                      Scaleway REASSIGN (migrator can't act for those owners).
#   SYSTEM_TRIGGER   — a customer trigger/policy on a platform-managed system
#                      table (users, tokens, storage_objects, vault_secrets,
#                      passkeys): can't be reassigned (the table stays
#                      migrator-owned); flag for manual handling.
#   MISSING_DDL_ROLE — a provisioned schema with no `_ddl` role.
#
# The platform helpers auth_uid/auth_role/auth_email are excluded (they stay
# migrator-owned and are re-granted as needed). This script never writes.
#
# Runs a one-off Job in-cluster as the developer role, same pattern as
# scripts/ops/check-sdk-func-readiness.sh.
#
# Usage: ./scripts/ops/check-console-ddl-readiness.sh
set -euo pipefail
NS=eurobase
JOB=console-ddl-readiness-check
IMG=rg.fr-par.scw.cloud/eurobase-app/migrations:latest

kubectl -n "$NS" delete job "$JOB" --ignore-not-found --wait=true >/dev/null 2>&1 || true
kubectl -n "$NS" apply -f - >/dev/null <<'YAML'
apiVersion: batch/v1
kind: Job
metadata: { name: console-ddl-readiness-check, namespace: eurobase }
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
            - name: CHECK_SQL
              value: |
                -- Tables / sequences / views / mat-views / foreign tables owned
                -- by the migrator in a tenant schema (relkinds r,p,S,v,m,f).
                SELECT 'MIGRATOR_OBJECT ' || n.nspname || '.' || c.relname || ' (' || c.relkind || ')'
                  FROM pg_class c
                  JOIN pg_namespace n ON n.oid = c.relnamespace
                  JOIN pg_roles r ON r.oid = c.relowner
                 WHERE n.nspname IN (SELECT schema_name FROM public.projects WHERE schema_name IS NOT NULL)
                   AND c.relkind IN ('r','p','S','v','m','f')
                   AND r.rolname = 'eurobase_migrator'
                   AND c.relname <> ALL (ARRAY['users','user_identities','refresh_tokens',
                       'email_tokens','storage_objects','storage_shared_prefixes','vault_secrets',
                       'user_passkey_credentials','webauthn_challenges'])
                 ORDER BY 1;
                -- Functions / procedures owned by the migrator (excl. the
                -- platform RLS helpers). SECURITY DEFINER flagged.
                SELECT 'MIGRATOR_OBJECT ' || n.nspname || '.' || p.proname || '()' ||
                       (CASE WHEN p.prosecdef THEN ' [SECURITY DEFINER]' ELSE '' END)
                  FROM pg_proc p
                  JOIN pg_namespace n ON n.oid = p.pronamespace
                  JOIN pg_roles r ON r.oid = p.proowner
                 WHERE n.nspname IN (SELECT schema_name FROM public.projects WHERE schema_name IS NOT NULL)
                   AND r.rolname = 'eurobase_migrator'
                   AND p.proname <> ALL (ARRAY['auth_uid','auth_role','auth_email'])
                 ORDER BY 1;
                -- Objects owned by developer or gateway: a Scaleway REASSIGN is
                -- needed (migrator can't act for those owners).
                SELECT 'FOREIGN_OBJECT ' || n.nspname || '.' || c.relname || ' owner=' || r.rolname
                  FROM pg_class c
                  JOIN pg_namespace n ON n.oid = c.relnamespace
                  JOIN pg_roles r ON r.oid = c.relowner
                 WHERE n.nspname IN (SELECT schema_name FROM public.projects WHERE schema_name IS NOT NULL)
                   AND c.relkind IN ('r','p','S','v','m','f')
                   AND r.rolname IN ('eurobase_developer','eurobase_gateway')
                 ORDER BY 1;
                -- Customer triggers on platform-managed system tables.
                SELECT 'SYSTEM_TRIGGER ' || n.nspname || '.' || c.relname || ' ' || t.tgname
                  FROM pg_trigger t
                  JOIN pg_class c ON c.oid = t.tgrelid
                  JOIN pg_namespace n ON n.oid = c.relnamespace
                 WHERE NOT t.tgisinternal
                   AND n.nspname IN (SELECT schema_name FROM public.projects WHERE schema_name IS NOT NULL)
                   AND c.relname = ANY (ARRAY['users','user_identities','refresh_tokens',
                       'email_tokens','storage_objects','storage_shared_prefixes','vault_secrets',
                       'user_passkey_credentials','webauthn_challenges'])
                 ORDER BY 1;
                -- Provisioned schema with no _ddl role.
                SELECT 'MISSING_DDL_ROLE ' || schema_name
                  FROM public.projects p
                 WHERE schema_name IS NOT NULL
                   AND EXISTS (SELECT 1 FROM pg_namespace n WHERE n.nspname = p.schema_name)
                   AND NOT EXISTS (SELECT 1 FROM pg_roles r WHERE r.rolname = p.schema_name || '_ddl')
                 ORDER BY 1;
YAML

kubectl -n "$NS" wait --for=condition=complete --timeout=120s "job/$JOB" >/dev/null 2>&1 || true
OUT=$(kubectl -n "$NS" logs "job/$JOB" 2>&1 || true)
kubectl -n "$NS" delete job "$JOB" --ignore-not-found >/dev/null 2>&1 || true

echo "$OUT" | grep -E '^(MIGRATOR_OBJECT|FOREIGN_OBJECT|SYSTEM_TRIGGER|MISSING_DDL_ROLE) ' | sort | uniq -c | awk '{print}'
echo ""
MIG=$(printf "%s\n" "$OUT" | grep -c '^MIGRATOR_OBJECT ' || true)
SECDEF=$(printf "%s\n" "$OUT" | grep -c 'SECURITY DEFINER]' || true)
FOREIGN=$(printf "%s\n" "$OUT" | grep -c '^FOREIGN_OBJECT ' || true)
SYSTRIG=$(printf "%s\n" "$OUT" | grep -c '^SYSTEM_TRIGGER ' || true)
MISSING=$(printf "%s\n" "$OUT" | grep -c '^MISSING_DDL_ROLE ' || true)

echo "Summary:"
echo "  migrator-owned objects to reassign to _ddl : $MIG (of which SECURITY DEFINER functions: $SECDEF)"
echo "  developer/gateway-owned (need Scaleway REASSIGN): $FOREIGN"
echo "  customer triggers on system tables (manual)     : $SYSTRIG"
echo "  schemas missing a _ddl role                     : $MISSING"
echo ""
if [ "$FOREIGN" != "0" ] || [ "$SYSTRIG" != "0" ] || [ "$MISSING" != "0" ]; then
  echo "ATTENTION: FOREIGN_OBJECT / SYSTEM_TRIGGER / MISSING_DDL_ROLE need handling before 6c converges." >&2
  exit 1
fi
echo "OK: only migrator-owned objects remain — the 6c convergence migration reassigns those to _ddl."
echo "    Review every [SECURITY DEFINER] function: reassigning changes who it runs as (migrator -> _ddl)."
