#!/usr/bin/env bash
# Step 2 of enabling tenant migrations (#190): let eurobase_migrator forward
# CONNECT to the per-tenant ddl roles. The migrate job's role can't grant
# this to itself (it has CONNECT but no grant option), and the database is
# owned by _rdb_superadmin — so this must run as your Scaleway DB admin user.
#
# Runs:  GRANT CONNECT ON DATABASE eurobase TO eurobase_migrator WITH GRANT OPTION;
#
# It connects as the admin user over a one-off in-cluster Job (the DB is
# reachable there; your laptop may be firewalled off). The admin password is
# read hidden and passed via a short-lived Secret, never put on the command
# line or into shell history.
#
# Assumes the operator has Secret-create/delete rights in the `eurobase`
# namespace (used for the short-lived PG* envFrom Secret and for the Job
# itself). Both are deleted by the EXIT trap below.
#
# The verification at the end is **gating**: a non-owner running `GRANT … WITH
# GRANT OPTION` against a Scaleway-owned DB returns a Postgres WARNING (not an
# error), so `ON_ERROR_STOP=1` would still exit 0 and a phase-only success
# check would print ✅ on a silent no-op (the same pattern that bit #217). The
# Job's second `-c` is a `DO $$ … RAISE EXCEPTION $$;` block that flips the
# Job to phase=Failed if the privilege didn't actually take.
#
# Usage: ./scripts/ops/grant-migrator-connect.sh [admin-user]   (default: eurobase)
set -euo pipefail

NS=eurobase
IMG=rg.fr-par.scw.cloud/eurobase-app/migrations:latest
ADMIN_USER="${1:-eurobase}"
TMP_SECRET=eb-admin-grant
JOB=eb-admin-grant

# Host/port/dbname from the existing migrator URL (no creds reused). Parse
# into separate fields so we can use PG* env vars instead of a URL — avoids
# URL-encoding pitfalls when the username is an email (@) or the password
# has special characters.
MIG_URL="$(kubectl -n "$NS" get secret eurobase-secrets -o jsonpath='{.data.DATABASE_URL_MIGRATOR}' | base64 -d)"
HOSTPART="$(printf '%s' "$MIG_URL" | sed -E 's#^[a-z]+://[^@]*@##')"   # host:port/db?params
HOSTPORT="${HOSTPART%%/*}"                                            # host:port
PGHOST="${HOSTPORT%%:*}"
PGPORT="${HOSTPORT##*:}"
DBQ="${HOSTPART#*/}"                                                  # db?params
PGDATABASE="${DBQ%%\?*}"
case "$MIG_URL" in *sslmode=*) PGSSLMODE="${MIG_URL##*sslmode=}"; PGSSLMODE="${PGSSLMODE%%&*}";; *) PGSSLMODE=require;; esac
echo "Target: host=${PGHOST} port=${PGPORT} db=${PGDATABASE} sslmode=${PGSSLMODE} user=${ADMIN_USER}"

read -r -s -p "Password for DB admin user '${ADMIN_USER}': " ADMIN_PW; echo
[ -n "$ADMIN_PW" ] || { echo "no password entered, aborting"; exit 1; }

cleanup() {
  kubectl -n "$NS" delete secret "$TMP_SECRET" --ignore-not-found >/dev/null 2>&1 || true
  kubectl -n "$NS" delete job "$JOB" --ignore-not-found >/dev/null 2>&1 || true
}
trap cleanup EXIT

kubectl -n "$NS" delete secret "$TMP_SECRET" --ignore-not-found >/dev/null 2>&1 || true
kubectl -n "$NS" create secret generic "$TMP_SECRET" \
  --from-literal=PGHOST="$PGHOST" \
  --from-literal=PGPORT="$PGPORT" \
  --from-literal=PGDATABASE="$PGDATABASE" \
  --from-literal=PGSSLMODE="$PGSSLMODE" \
  --from-literal=PGUSER="$ADMIN_USER" \
  --from-literal=PGPASSWORD="$ADMIN_PW" >/dev/null
unset ADMIN_PW

kubectl -n "$NS" delete job "$JOB" --ignore-not-found --wait=true >/dev/null 2>&1 || true
kubectl -n "$NS" apply -f - >/dev/null <<YAML
apiVersion: batch/v1
kind: Job
metadata: { name: ${JOB}, namespace: ${NS} }
spec:
  backoffLimit: 0
  ttlSecondsAfterFinished: 120
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: grant
          image: ${IMG}
          imagePullPolicy: Always
          command:
            - sh
            - -c
            - |
              psql -v ON_ERROR_STOP=1 \
                -c "GRANT CONNECT ON DATABASE eurobase TO eurobase_migrator WITH GRANT OPTION;" \
                -c "SELECT 'migrator_canconnectgrant=' || has_database_privilege('eurobase_migrator','eurobase','CONNECT WITH GRANT OPTION');" \
                -c "DO \$\$ BEGIN IF NOT has_database_privilege('eurobase_migrator','eurobase','CONNECT WITH GRANT OPTION') THEN RAISE EXCEPTION 'grant did not take — the DB is owned by _rdb_superadmin, ask Scaleway support or run as _rdb_superadmin'; END IF; END \$\$;"
          envFrom:
            - secretRef: { name: ${TMP_SECRET} }
YAML

echo "==> running the grant as ${ADMIN_USER}..."
for _ in $(seq 1 30); do
  p=$(kubectl -n "$NS" get pods -l job-name=${JOB} -o jsonpath='{.items[0].status.phase}' 2>/dev/null || true)
  [ "$p" = "Succeeded" ] || [ "$p" = "Failed" ] && break; sleep 2
done
echo "==> output:"
kubectl -n "$NS" logs job/${JOB} 2>&1 || true
phase=$(kubectl -n "$NS" get pods -l job-name=${JOB} -o jsonpath='{.items[0].status.phase}' 2>/dev/null || true)
echo
if [ "$phase" = "Succeeded" ]; then
  # Job phase=Succeeded is meaningful: the DO-block assertion above makes the
  # Job fail if the privilege didn't actually take, even though Postgres would
  # only WARN on a silent-no-op grant by a non-owner.
  echo "✅ Granted. eurobase_migrator can now forward CONNECT to per-tenant ddl roles."
  echo "   Tenant migrations are fully enabled — the gateway grants each tenant's"
  echo "   CONNECT on first 'eurobase migrations up'. No redeploy needed."
else
  echo "⚠️  The grant did not succeed (phase=${phase}). See the Job output above."
  echo "   - 'permission denied' → the '${ADMIN_USER}' user isn't the DB owner."
  echo "   - 'grant did not take' → the GRANT ran without error but the privilege"
  echo "     didn't land (the DB is owned by _rdb_superadmin and silently ignores"
  echo "     non-owner grants). Open a Scaleway support ticket asking them to run"
  echo "     the GRANT as _rdb_superadmin (see docs/runbooks/tenant-migrations.md)."
  exit 1
fi
