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
#   SYSTEM_TRIGGER /  — a CUSTOMER-ADDED trigger or RLS policy on a
#   SYSTEM_POLICY      platform-managed system table (the platform's own
#                      policies are excluded by name): can't be reassigned
#                      (the table stays migrator-owned); flag for manual review.
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
                -- Tables / views / mat-views / foreign tables owned by the
                -- migrator in a tenant schema (relkinds r,p,v,m,f).
                SELECT 'MIGRATOR_OBJECT ' || n.nspname || '.' || c.relname || ' (' || c.relkind || ')'
                  FROM pg_class c
                  JOIN pg_namespace n ON n.oid = c.relnamespace
                  JOIN pg_roles r ON r.oid = c.relowner
                 WHERE n.nspname IN (SELECT schema_name FROM public.projects WHERE schema_name IS NOT NULL)
                   AND c.relkind IN ('r','p','v','m','f')
                   AND r.rolname = 'eurobase_migrator'
                   AND c.relname <> ALL (ARRAY['users','user_identities','refresh_tokens',
                       'email_tokens','storage_objects','storage_shared_prefixes','vault_secrets',
                       'user_passkey_credentials','webauthn_challenges'])
                 ORDER BY 1;
                -- Sequences owned by the migrator, excluding those auto-owned
                -- by a system table (their *_id_seq would otherwise show as a
                -- reassign candidate, but they follow their table's ownership).
                SELECT 'MIGRATOR_OBJECT ' || n.nspname || '.' || c.relname || ' (S)'
                  FROM pg_class c
                  JOIN pg_namespace n ON n.oid = c.relnamespace
                  JOIN pg_roles r ON r.oid = c.relowner
                 WHERE n.nspname IN (SELECT schema_name FROM public.projects WHERE schema_name IS NOT NULL)
                   AND c.relkind = 'S'
                   AND r.rolname = 'eurobase_migrator'
                   AND NOT EXISTS (
                       SELECT 1 FROM pg_depend d
                        JOIN pg_class t ON t.oid = d.refobjid
                       WHERE d.objid = c.oid AND d.deptype IN ('a','i')
                         AND t.relname = ANY (ARRAY['users','user_identities','refresh_tokens',
                             'email_tokens','storage_objects','storage_shared_prefixes','vault_secrets',
                             'user_passkey_credentials','webauthn_challenges']))
                 ORDER BY 1;
                -- Standalone types (composite/enum/domain) owned by the migrator.
                SELECT 'MIGRATOR_OBJECT ' || n.nspname || '.' || t.typname || ' (type ' || t.typtype || ')'
                  FROM pg_type t
                  JOIN pg_namespace n ON n.oid = t.typnamespace
                  JOIN pg_roles r ON r.oid = t.typowner
                 WHERE n.nspname IN (SELECT schema_name FROM public.projects WHERE schema_name IS NOT NULL)
                   AND t.typtype IN ('c','e','d')
                   AND r.rolname = 'eurobase_migrator'
                   AND NOT EXISTS (SELECT 1 FROM pg_class c WHERE c.reltype = t.oid AND c.relkind <> 'c')
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
                -- Customer RLS policies on platform-managed system tables
                -- (these aren't the platform's own owner-only / RLS policies —
                -- flag any extra ones for manual handling).
                SELECT 'SYSTEM_POLICY ' || n.nspname || '.' || c.relname || ' ' || pol.polname
                  FROM pg_policy pol
                  JOIN pg_class c ON c.oid = pol.polrelid
                  JOIN pg_namespace n ON n.oid = c.relnamespace
                 WHERE n.nspname IN (SELECT schema_name FROM public.projects WHERE schema_name IS NOT NULL)
                   AND c.relname = ANY (ARRAY['users','user_identities','refresh_tokens',
                       'email_tokens','storage_objects','storage_shared_prefixes','vault_secrets',
                       'user_passkey_credentials','webauthn_challenges'])
                   -- Exclude the platform's OWN system-table policies (created by
                   -- provision_tenant / the system-table migrations); only a
                   -- customer-added policy on a system table is a finding.
                   AND pol.polname <> ALL (ARRAY[
                       'email_tokens_policy','refresh_tokens_policy','user_identities_policy',
                       'vault_secrets_policy','webauthn_challenges_policy','user_self_access',
                       'storage_read','storage_insert','storage_update','storage_delete',
                       'storage_owner_access','shared_prefixes_read','shared_prefixes_write',
                       'passkey_select','passkey_insert','passkey_update','passkey_delete',
                       'tenant_isolation_users','tenant_isolation_storage'])
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

echo "$OUT" | grep -E '^(MIGRATOR_OBJECT|FOREIGN_OBJECT|SYSTEM_TRIGGER|SYSTEM_POLICY|MISSING_DDL_ROLE) ' | sort
echo ""
MIG=$(printf "%s\n" "$OUT" | grep -c '^MIGRATOR_OBJECT ' || true)
SECDEF=$(printf "%s\n" "$OUT" | grep -c 'SECURITY DEFINER]' || true)
FOREIGN=$(printf "%s\n" "$OUT" | grep -c '^FOREIGN_OBJECT ' || true)
SYSTRIG=$(printf "%s\n" "$OUT" | grep -c '^SYSTEM_TRIGGER ' || true)
SYSPOL=$(printf "%s\n" "$OUT" | grep -c '^SYSTEM_POLICY ' || true)
MISSING=$(printf "%s\n" "$OUT" | grep -c '^MISSING_DDL_ROLE ' || true)

echo "Summary:"
echo "  migrator-owned objects to reassign to _ddl : $MIG (of which SECURITY DEFINER functions: $SECDEF)"
echo "  developer/gateway-owned (need Scaleway REASSIGN): $FOREIGN"
echo "  customer triggers on system tables (manual)     : $SYSTRIG"
echo "  customer RLS policies on system tables (manual)  : $SYSPOL"
echo "  schemas missing a _ddl role                     : $MISSING"
echo ""
if [ "$FOREIGN" != "0" ] || [ "$SYSTRIG" != "0" ] || [ "$SYSPOL" != "0" ] || [ "$MISSING" != "0" ]; then
  echo "ATTENTION: FOREIGN_OBJECT / SYSTEM_TRIGGER / SYSTEM_POLICY / MISSING_DDL_ROLE need handling before 6c converges." >&2
  exit 1
fi
echo "OK: only migrator-owned objects remain — the 6c convergence migration reassigns those to _ddl."
echo "    Review every [SECURITY DEFINER] function: reassigning changes who it runs as (migrator -> _ddl)."
