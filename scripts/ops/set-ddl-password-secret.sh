#!/usr/bin/env bash
# Enable tenant migrations (#190) by adding DDL_PASSWORD_SECRET to the
# eurobase-secrets Secret and restarting the gateway to pick it up.
#
# DDL_PASSWORD_SECRET is the master secret the gateway uses to derive each
# per-tenant ddl role's login password (HMAC-SHA256). It only needs to be
# set once and kept stable — rotating it changes every derived password
# (the gateway re-sets them on the next apply, so rotation is safe, just
# means a brief re-derive).
#
# NOTE: tenant migrations ALSO require eurobase_migrator to own the
# `eurobase` database (or hold CONNECT … WITH GRANT OPTION) — without it
# the apply path fails loud at first use. That's a separate DB-owner step.
#
# Usage: ./scripts/ops/set-ddl-password-secret.sh
set -euo pipefail

NS=eurobase
SECRET=eurobase-secrets
KEY=DDL_PASSWORD_SECRET

if kubectl -n "$NS" get secret "$SECRET" -o jsonpath="{.data.$KEY}" 2>/dev/null | grep -q .; then
  echo "⚠️  $KEY already exists in $SECRET. Overwriting it would invalidate"
  echo "    every per-tenant ddl password until the next apply re-derives them."
  read -r -p "    overwrite? [y/N] " ans
  [ "$ans" = "y" ] || [ "$ans" = "Y" ] || { echo "aborted (left existing value untouched)"; exit 1; }
fi

VALUE="$(openssl rand -hex 32)"   # 32 bytes, hex-encoded
echo "==> setting $KEY (64 hex chars) in $SECRET"
kubectl -n "$NS" patch secret "$SECRET" --type merge \
  -p "{\"stringData\":{\"$KEY\":\"$VALUE\"}}"

echo "==> restarting the gateway to load the new env (envFrom injects only at pod start)"
kubectl -n "$NS" rollout restart deployment/gateway
kubectl -n "$NS" rollout status deployment/gateway --timeout=120s

echo
echo "✅ $KEY set and gateway rolled."
echo "   Tenant migrations are enabled IF eurobase_migrator also owns the"
echo "   eurobase database. Confirm with:"
echo "     ./scripts/ops/migrate-status.sh   # (the gateway log will say 'tenant migrations enabled')"
echo "   and test from a project:  eurobase migrations up"
