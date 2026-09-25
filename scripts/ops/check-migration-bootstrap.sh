#!/usr/bin/env bash
# Read-only: report the production facts scripts/db/apply-migrations.sh
# assumes for a fresh environment (#632), so the replay's "console
# bootstrap" can be checked against what Scaleway prod actually has.
# Runs a one-off Job in-cluster (the prod DB is only reachable there) as
# eurobase_migrator, same pattern as scripts/ops/db-checks.sh.
#
# Usage: ./scripts/ops/check-migration-bootstrap.sh
set -euo pipefail
NS=eurobase
JOB=migration-bootstrap-check
IMG=rg.fr-par.scw.cloud/eurobase-app/migrations:latest

kubectl -n "$NS" delete job "$JOB" --ignore-not-found --wait=true >/dev/null 2>&1 || true
kubectl -n "$NS" apply -f - >/dev/null <<'YAML'
apiVersion: batch/v1
kind: Job
metadata: { name: migration-bootstrap-check, namespace: eurobase }
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
          command: ["sh", "-c", 'printf "%s\n" "$CHECK_SQL" | psql "$DATABASE_URL_MIGRATOR" -tA']
          env:
            - name: DATABASE_URL_MIGRATOR
              valueFrom: { secretKeyRef: { name: eurobase-secrets, key: DATABASE_URL_MIGRATOR } }
            - name: CHECK_SQL
              value: |
                SELECT 'schema_migrations=' || version || CASE WHEN dirty THEN ' (DIRTY)' ELSE '' END FROM schema_migrations;
                SELECT 'role ' || rolname || ': inherit=' || rolinherit || ' createrole=' || rolcreaterole || ' super=' || rolsuper
                  FROM pg_roles WHERE rolname IN ('eurobase_api','eurobase_migrator','eurobase_gateway','eurobase_developer','eurobase_function_runner') ORDER BY rolname;
                SELECT 'member ' || m.rolname || ' IN ' || r.rolname || ': inherit=' || am.inherit_option || ' set=' || am.set_option || ' admin=' || am.admin_option
                  FROM pg_auth_members am JOIN pg_roles r ON r.oid = am.roleid JOIN pg_roles m ON m.oid = am.member
                 WHERE r.rolname IN ('eurobase_api','eurobase_migrator','eurobase_gateway','eurobase_developer','eurobase_function_runner')
                   AND m.rolname IN ('eurobase_migrator','eurobase_gateway','eurobase_developer','eurobase_function_runner','eurobase_api')
                 ORDER BY 1;
                SELECT 'migrator db: CONNECT grantable=' || has_database_privilege('eurobase_migrator', current_database(), 'CONNECT WITH GRANT OPTION')
                    || ' CREATE=' || has_database_privilege('eurobase_migrator', current_database(), 'CREATE');
                SELECT 'migrator schema public: USAGE grantable=' || has_schema_privilege('eurobase_migrator', 'public', 'USAGE WITH GRANT OPTION')
                    || ' CREATE=' || has_schema_privilege('eurobase_migrator', 'public', 'CREATE');
                SELECT 'index: ' || indexdef FROM pg_indexes WHERE indexname IN ('ix_retention_holds_lookup', 'ix_retention_holds_expiry') ORDER BY indexname;
                SELECT 'constraint retention_holds_legal_basis_check: validated=' || convalidated FROM pg_constraint WHERE conname = 'retention_holds_legal_basis_check';
                SELECT 'runner tenant _func memberships (target 0, see runner-membership-revoke.sql): ' || count(*)
                  FROM pg_auth_members am JOIN pg_roles r ON r.oid = am.roleid
                 WHERE am.member = 'eurobase_function_runner'::regrole AND r.rolname ~ '^tenant_[0-9a-f_]+_func$';
                SELECT 'auth.email() live lookup: ' || (pg_get_functiondef('auth.email()'::regprocedure) LIKE '%FROM users%');
YAML

for _ in $(seq 1 45); do
  p=$(kubectl -n "$NS" get pods -l job-name=${JOB} -o jsonpath='{.items[0].status.phase}' 2>/dev/null || true)
  [ "$p" = "Succeeded" ] || [ "$p" = "Failed" ] && break; sleep 2
done
kubectl -n "$NS" logs job/${JOB} 2>&1
kubectl -n "$NS" delete job "$JOB" --ignore-not-found >/dev/null 2>&1 || true
