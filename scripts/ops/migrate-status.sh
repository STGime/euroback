#!/usr/bin/env bash
# Show the current migration version + dirty flag in production, by running
# `migrate version` as a one-off Job inside the cluster (DATABASE_URL_MIGRATOR
# from the eurobase-secrets Secret; the prod DB is reachable only there).
#
# Usage: ./scripts/ops/migrate-status.sh
set -euo pipefail

NS=eurobase
JOB=migrate-version
IMG=rg.fr-par.scw.cloud/eurobase-app/migrations:latest

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
          args: ["-database", "\$(DATABASE_URL_MIGRATOR)", "version"]
          env:
            - name: DATABASE_URL_MIGRATOR
              valueFrom:
                secretKeyRef:
                  name: eurobase-secrets
                  key: DATABASE_URL_MIGRATOR
YAML

echo "==> waiting for the version check to run..."
# Wait for the pod to finish (complete or fail), then print its logs.
for _ in $(seq 1 30); do
  phase=$(kubectl -n "$NS" get pods -l job-name=${JOB} -o jsonpath='{.items[0].status.phase}' 2>/dev/null || true)
  [ "$phase" = "Succeeded" ] || [ "$phase" = "Failed" ] && break
  sleep 2
done

echo "==> migrate version output:"
kubectl -n "$NS" logs job/${JOB}
echo
echo "(If it shows '63' with a dirty/error line, run ./scripts/ops/migrate-force.sh next.)"
kubectl -n "$NS" delete job "$JOB" --ignore-not-found >/dev/null 2>&1 || true
