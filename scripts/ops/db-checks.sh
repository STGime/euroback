#!/usr/bin/env bash
# Read-only production DB checks for tenant migrations (#190). Runs a one-off
# Job in-cluster (the prod DB is only reachable there) as eurobase_migrator.
# Use to confirm step 2 is done (migrator_canconnectgrant=true) after Scaleway
# runs the CONNECT grant, and to sanity-check the role/db state.
#
# Usage: ./scripts/ops/db-checks.sh
set -euo pipefail

NS=eurobase
JOB=db-checks
IMG=rg.fr-par.scw.cloud/eurobase-app/migrations:latest

kubectl -n "$NS" delete job "$JOB" --ignore-not-found --wait=true >/dev/null 2>&1 || true
kubectl -n "$NS" apply -f - >/dev/null <<YAML
apiVersion: batch/v1
kind: Job
metadata: { name: ${JOB}, namespace: ${NS} }
spec:
  backoffLimit: 0
  ttlSecondsAfterFinished: 200
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: c
          image: ${IMG}
          imagePullPolicy: Always
          command: ["sh","-c","psql \"\$DATABASE_URL_MIGRATOR\" -tAc \"SELECT 'db_owner=' || pg_get_userbyid(datdba) FROM pg_database WHERE datname=current_database(); SELECT 'migrator_canconnectgrant=' || has_database_privilege('eurobase_migrator','eurobase','CONNECT WITH GRANT OPTION'); SELECT 'ddl_roles=' || count(*) FROM pg_roles WHERE rolname LIKE 'tenant_%\\_ddl';\""]
          env:
            - name: DATABASE_URL_MIGRATOR
              valueFrom: { secretKeyRef: { name: eurobase-secrets, key: DATABASE_URL_MIGRATOR } }
YAML

for _ in $(seq 1 30); do
  p=$(kubectl -n "$NS" get pods -l job-name=${JOB} -o jsonpath='{.items[0].status.phase}' 2>/dev/null || true)
  [ "$p" = "Succeeded" ] || [ "$p" = "Failed" ] && break; sleep 2
done
kubectl -n "$NS" logs job/${JOB} 2>&1 | grep -E "db_owner|canconnectgrant|ddl_roles" || kubectl -n "$NS" logs job/${JOB} 2>&1
kubectl -n "$NS" delete job "$JOB" --ignore-not-found >/dev/null 2>&1 || true
echo
echo "migrator_canconnectgrant=true  → step 2 done, tenant migrations can grant per-tenant CONNECT."
