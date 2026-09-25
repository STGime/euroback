#!/usr/bin/env bash
# Enable per-tenant logins for edge-function SQL (stage B phase 1a) by
# adding FUNC_PASSWORD_SECRET to the eurobase-secrets Secret and restarting
# the worker (applies the tenant logins) and then the functions runner
# (starts connecting as each tenant). See AGENTS.md "Per-tenant function
# logins".
#
# The value is generated here and never printed. Keep it stable: rotating
# it changes every derived login; the worker re-applies all of them within
# minutes and the runner falls back to the shared connection meanwhile.
#
# Usage: ./scripts/ops/set-func-password-secret.sh
set -euo pipefail
NS=eurobase
SECRET=eurobase-secrets
KEY=FUNC_PASSWORD_SECRET

if kubectl -n "$NS" get secret "$SECRET" -o jsonpath="{.data.$KEY}" 2>/dev/null | grep -q .; then
  echo "⚠️  $KEY already exists in $SECRET. Overwriting rotates every tenant"
  echo "    function login (the runner falls back until the worker re-applies)."
  read -r -p "    overwrite? [y/N] " ans
  [ "$ans" = "y" ] || [ "$ans" = "Y" ] || { echo "aborted (left existing value untouched)"; exit 1; }
fi

VALUE="$(openssl rand -hex 32)"   # 32 bytes, hex-encoded
echo "==> setting $KEY (64 hex chars) in $SECRET"
kubectl -n "$NS" patch secret "$SECRET" --type merge \
  -p "{\"stringData\":{\"$KEY\":\"$VALUE\"}}" >/dev/null
unset VALUE

echo "==> restarting the worker (applies tenant logins at startup)"
kubectl -n "$NS" rollout restart deployment/worker
kubectl -n "$NS" rollout status deployment/worker --timeout=180s

echo "==> waiting for the worker to report the logins"
for _ in $(seq 1 30); do
  line="$(kubectl -n "$NS" logs deployment/worker --since=5m 2>/dev/null | grep -E 'tenant function logins (ensured|: some roles failed)|tenant function logins misconfigured' | tail -1 || true)"
  [ -n "$line" ] && break
  sleep 4
done
echo "    ${line:-no log line yet — check: kubectl -n $NS logs deployment/worker | grep 'tenant function logins'}"

echo "==> restarting the functions runner (starts per-tenant connections)"
kubectl -n "$NS" rollout restart deployment/functions
kubectl -n "$NS" rollout status deployment/functions --timeout=180s

echo
echo "✅ $KEY set; worker and functions restarted."
echo "   Next: invoke a function and check the runner logs for '[tenant-db]' fallback warnings."
