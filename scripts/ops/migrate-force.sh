#!/usr/bin/env bash
# Clear the dirty migration flag in production by forcing the schema version,
# via a one-off Job inside the cluster. `migrate force <V>` only rewrites the
# schema_migrations bookkeeping (version=V, dirty=false) — it runs NO SQL and
# changes NO schema.
#
# Pass the last GOOD version (the one before the dirty one migrate-status
# shows). After this, the next deploy's `migrate up` re-applies everything
# above it. There is deliberately no default: the old default (62, from the
# 000063 incident) would today make `up` re-run 63–124 against tables that
# already exist and leave the DB dirty again (#632 review).
#
# Usage: ./scripts/ops/migrate-force.sh <version>
set -euo pipefail

VERSION="${1:?usage: migrate-force.sh <last-good-version> — check scripts/ops/migrate-status.sh first}"
NS=eurobase
JOB=migrate-force
IMG=rg.fr-par.scw.cloud/eurobase-app/migrations:latest

echo "==> forcing migration version to ${VERSION} (no schema change)"
read -r -p "    proceed? [y/N] " ans
[ "$ans" = "y" ] || [ "$ans" = "Y" ] || { echo "aborted"; exit 1; }

kubectl -n "$NS" delete job "$JOB" --ignore-not-found --wait=true >/dev/null 2>&1 || true

kubectl -n "$NS" apply -f - <<YAML
apiVersion: batch/v1
kind: Job
metadata:
  name: ${JOB}
  namespace: ${NS}
spec:
  backoffLimit: 0
  ttlSecondsAfterFinished: 600
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: migrate
          image: ${IMG}
          imagePullPolicy: Always
          args: ["-database", "\$(DATABASE_URL_MIGRATOR)", "force", "${VERSION}"]
          env:
            - name: DATABASE_URL_MIGRATOR
              valueFrom:
                secretKeyRef:
                  name: eurobase-secrets
                  key: DATABASE_URL_MIGRATOR
YAML

echo "==> waiting for the force to run..."
for _ in $(seq 1 30); do
  phase=$(kubectl -n "$NS" get pods -l job-name=${JOB} -o jsonpath='{.items[0].status.phase}' 2>/dev/null || true)
  [ "$phase" = "Succeeded" ] || [ "$phase" = "Failed" ] && break
  sleep 2
done

echo "==> force output:"
kubectl -n "$NS" logs job/${JOB} || true
phase=$(kubectl -n "$NS" get pods -l job-name=${JOB} -o jsonpath='{.items[0].status.phase}' 2>/dev/null || true)
kubectl -n "$NS" delete job "$JOB" --ignore-not-found >/dev/null 2>&1 || true
echo
if [ "$phase" = "Succeeded" ]; then
  echo "✅ Dirty flag cleared at version ${VERSION}. The fixed 000063 will apply on the next deploy."
else
  echo "⚠️  Force job did not succeed (phase=${phase}). Check the output above before redeploying."
  exit 1
fi
