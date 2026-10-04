#!/usr/bin/env bash
# Read-only smoke test of the in-cluster PgBouncers: from a one-off pod,
# connect as one tenant's `<schema>_func` role (derived password — SCRAM
# pass-through) through BOTH poolers' tenant alias (the runner's and the
# gateway's — the gateway's now serves tenant _func for the SDK path), and
# check both poolers refuse the gateway platform role. SELECTs only.
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
function pooled(raw: string, host: string, user?: string, pw?: string): string {
  const u = new URL(raw);
  u.hostname = host + ".eurobase.svc.cluster.local"; u.port = "6432";
  u.searchParams.set("sslmode", "disable"); // in-cluster hop; pooler → RDB uses TLS
  // Tenants use the tenant alias (its own server-connection cap), like the runner.
  if (user) { u.pathname = u.pathname.replace(/\/?$/, "") + "_tenant"; u.username = encodeURIComponent(user); u.password = encodeURIComponent(pw!); }
  return u.toString();
}
async function check(label: string, url: string, q: string, expectFail = false) {
  const sql = postgres(url, { max: 1 });
  try {
    const [r] = await sql.unsafe(q);
    console.log(expectFail ? `FAIL ${label}: unexpectedly accepted` : `OK   ${label}: ${JSON.stringify(r)}`);
  } catch (e) {
    console.log(expectFail ? `OK   ${label}: refused (${(e as Error).message})` : `FAIL ${label}: ${(e as any).code ?? ""} ${(e as Error).message}`);
  } finally { await sql.end({ timeout: 5 }); }
}
const gw = Deno.env.get("DATABASE_URL")!;
const runner = Deno.env.get("DATABASE_URL_FUNCTION_RUNNER")!;
// Both poolers now serve tenant `_func` only and hold no platform
// passwords: the gateway platform role is refused by both.
await check("gateway role via pgbouncer-gateway (refused)", pooled(gw, "pgbouncer-gateway"), "SELECT 1", true);
await check("gateway role via runner pooler (refused)", pooled(gw, "pgbouncer"), "SELECT 1", true);
// The tenant schema is discovered host-side (the smoke pod has no Scaleway
// CA to verify a direct RDB TLS connection, and the poolers serve no
// platform role) and passed in via SMOKE_TENANT_SCHEMA.
const schema = Deno.env.get("SMOKE_TENANT_SCHEMA") ?? "";
const t = schema ? { s: schema } : null;
if (t) {
  const pw = await funcPassword(Deno.env.get("FUNC_PASSWORD_SECRET")!, t.s);
  // The tenant _func login works through BOTH poolers' tenant alias.
  await check(`tenant ${t.s.slice(0, 15)}… via runner pooler`, pooled(runner, "pgbouncer", t.s + "_func", pw), "SELECT session_user");
  await check(`tenant ${t.s.slice(0, 15)}… via gateway pooler`, pooled(gw, "pgbouncer-gateway", t.s + "_func", pw), "SELECT session_user");
} else {
  console.log("SKIP tenant checks: no tenant schema found");
}
EOF
# Discover a tenant schema in-cluster (psql trusts sslmode=require without a
# CA; the smoke pod's Deno runtime can't). Short-lived migrations-image pod
# as the developer role.
MIGIMG=$(kubectl -n "$NS" get job migrate -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null || echo "rg.fr-par.scw.cloud/eurobase-app/migrations:latest")
DISC="$POD-disc"
kubectl -n "$NS" delete pod "$DISC" --ignore-not-found --wait=true >/dev/null 2>&1 || true
kubectl -n "$NS" apply -f - >/dev/null <<YAML
apiVersion: v1
kind: Pod
metadata: { name: $DISC, namespace: $NS, labels: { app: pgbouncer-smoke } }
spec:
  restartPolicy: Never
  containers:
    - name: c
      image: $MIGIMG
      command: ["sh","-c","psql \"\$U\" -tAc \"SELECT n.nspname FROM pg_namespace n JOIN pg_roles r ON r.rolname = n.nspname || '_func' AND r.rolcanlogin WHERE n.nspname ~ '^tenant_[0-9a-f_]+\$' ORDER BY 1 LIMIT 1\""]
      env:
        - { name: U, valueFrom: { secretKeyRef: { name: eurobase-secrets, key: DATABASE_URL_DEVELOPER } } }
YAML
for _ in $(seq 1 40); do dp=$(kubectl -n "$NS" get pod "$DISC" -o jsonpath='{.status.phase}' 2>/dev/null); [ "$dp" = Succeeded ] || [ "$dp" = Failed ] && break; sleep 2; done
TENANT_SCHEMA=$(kubectl -n "$NS" logs "$DISC" 2>/dev/null | tr -d '[:space:]')
kubectl -n "$NS" delete pod "$DISC" --ignore-not-found >/dev/null 2>&1
[ -n "$TENANT_SCHEMA" ] && echo "discovered tenant schema: $TENANT_SCHEMA" || echo "no tenant schema found (tenant checks will skip)"

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
        - { name: SMOKE_TENANT_SCHEMA, value: "$TENANT_SCHEMA" }
      volumeMounts: [{ name: smoke, mountPath: /smoke }]
  volumes: [{ name: smoke, configMap: { name: $POD } }]
YAML
for _ in $(seq 1 60); do p=$(kubectl -n "$NS" get pod "$POD" -o jsonpath='{.status.phase}' 2>/dev/null); [ "$p" = Succeeded ] || [ "$p" = Failed ] && break; sleep 2; done
ALL=$(kubectl -n "$NS" logs "$POD" 2>&1)
echo "$ALL" | grep -E "^(OK|FAIL|SKIP)" || true
if [ "${p:-}" = Failed ] || ! echo "$ALL" | grep -q "^OK" || echo "$ALL" | grep -q "^FAIL"; then
  echo "pgbouncer smoke test FAILED (pod phase: ${p:-unknown})" >&2
  echo "$ALL" | tail -20 >&2
  exit 1
fi
