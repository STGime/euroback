#!/usr/bin/env bash
# Read-only smoke test of the in-cluster PgBouncer (#641, PR 2): from a
# one-off pod, connect through pgbouncer:6432 as the gateway role, the
# function-runner role and one tenant's `<schema>_func` role (derived
# password — the pooler passes SCRAM through), and run SELECTs only.
# Secrets are read from eurobase-secrets inside the pod, never printed.
#
# Usage: ./scripts/ops/pgbouncer-smoke.sh
set -euo pipefail
NS=eurobase
POD=pgbouncer-smoke
IMG=$(kubectl -n "$NS" get deploy functions -o jsonpath='{.spec.template.spec.containers[0].image}')

kubectl -n "$NS" delete pod "$POD" --ignore-not-found --wait=true >/dev/null 2>&1 || true
kubectl -n "$NS" delete configmap "$POD" --ignore-not-found >/dev/null 2>&1 || true
TMP=$(mktemp -d); trap 'rm -rf "$TMP"; kubectl -n "$NS" delete pod "$POD" --ignore-not-found >/dev/null 2>&1; kubectl -n "$NS" delete configmap "$POD" --ignore-not-found >/dev/null 2>&1' EXIT
cat > "$TMP/smoke.ts" <<'EOF'
import { funcPassword } from "/app/tenant_db.ts";
const { default: postgres } = await import("https://deno.land/x/postgresjs@v3.4.7/mod.js");
function pooled(raw: string, user?: string, pw?: string): string {
  const u = new URL(raw);
  u.hostname = "pgbouncer.eurobase.svc.cluster.local"; u.port = "6432";
  // Tenants use the tenant alias (its own server-connection cap), like the runner.
  if (user) u.pathname = u.pathname.replace(/\/?$/, "") + "_tenant";
  u.searchParams.set("sslmode", "disable"); // in-cluster hop; pooler → RDB uses TLS
  if (user) { u.username = encodeURIComponent(user); u.password = encodeURIComponent(pw!); }
  return u.toString();
}
async function check(label: string, url: string, q: string) {
  const sql = postgres(url, { max: 1 });
  try { const [r] = await sql.unsafe(q); console.log(`OK   ${label}: ${JSON.stringify(r)}`); }
  catch (e) { console.log(`FAIL ${label}: ${(e as any).code ?? ""} ${(e as Error).message}`); }
  finally { await sql.end({ timeout: 5 }); }
}
const gw = Deno.env.get("DATABASE_URL")!;
const runner = Deno.env.get("DATABASE_URL_FUNCTION_RUNNER")!;
await check("gateway", pooled(gw), "SELECT current_user, now()");
await check("function runner", pooled(runner), "SELECT current_user");
const g = postgres(pooled(gw), { max: 1 });
const [t] = await g`SELECT n.nspname AS s FROM pg_namespace n JOIN pg_roles r ON r.rolname = n.nspname || '_func' AND r.rolcanlogin WHERE n.nspname ~ '^tenant_[0-9a-f_]+$' ORDER BY 1 LIMIT 1`;
await g.end({ timeout: 5 });
if (t) {
  const pw = await funcPassword(Deno.env.get("FUNC_PASSWORD_SECRET")!, t.s);
  await check(`tenant ${t.s.slice(0, 15)}…`, pooled(runner, t.s + "_func", pw), "SELECT session_user");
}
EOF
kubectl -n "$NS" create configmap "$POD" --from-file=smoke.ts="$TMP/smoke.ts" >/dev/null
kubectl -n "$NS" apply -f - >/dev/null <<YAML
apiVersion: v1
kind: Pod
metadata: { name: $POD, namespace: $NS, labels: { app: pgbouncer-smoke } }
spec:
  restartPolicy: Never
  containers:
    - name: c
      image: $IMG
      command: ["deno", "run", "--allow-net", "--allow-env", "--allow-read", "/smoke/smoke.ts"]
      env:
        - { name: DATABASE_URL, valueFrom: { secretKeyRef: { name: eurobase-secrets, key: DATABASE_URL } } }
        - { name: DATABASE_URL_FUNCTION_RUNNER, valueFrom: { secretKeyRef: { name: eurobase-secrets, key: DATABASE_URL_FUNCTION_RUNNER } } }
        - { name: FUNC_PASSWORD_SECRET, valueFrom: { secretKeyRef: { name: eurobase-secrets, key: FUNC_PASSWORD_SECRET } } }
      volumeMounts: [{ name: smoke, mountPath: /smoke }]
  volumes: [{ name: smoke, configMap: { name: $POD } }]
YAML
for _ in $(seq 1 60); do p=$(kubectl -n "$NS" get pod "$POD" -o jsonpath='{.status.phase}' 2>/dev/null); [ "$p" = Succeeded ] || [ "$p" = Failed ] && break; sleep 2; done
OUT=$(kubectl -n "$NS" logs "$POD" 2>&1 | grep -E "^(OK|FAIL)" || true)
echo "$OUT"
if [ -z "$OUT" ] || echo "$OUT" | grep -q "^FAIL"; then
  echo "pgbouncer smoke test FAILED (pod phase: ${p:-unknown})" >&2
  exit 1
fi
