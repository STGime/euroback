# Cost-tier runbook — beta vs production

Eurobase has two configurations:

- **Beta** — single Kapsule node, replicas=1 across the gateway / mcp-server / functions, in-cluster Redis. Runs at ~€45/mo on Scaleway.
- **Production** — two Kapsule nodes, replicas=2 on the public-path deployments, managed Redis. Runs at ~€127/mo on Scaleway and tolerates a node restart without downtime.

We're on **beta** today. Bump to production when the first paying Pro customer signs up (issue #135).

## Beta → production (when revenue justifies it)

Do these in order. Each step is independently reversible.

### Step 1 — bump Kapsule pool to 2 nodes (~€28/mo)

Restores redundancy on the gateway path. Do this **first** — paying users get HA before they get faster scaling.

```bash
# Pick the pool ID from the console: Kubernetes → eurobase-cluster → Node pools
scw k8s pool update <pool-id> autoscaler.min-size=1 autoscaler.max-size=2

# Or, via the console: Kapsule → cluster → pool → Edit → Autoscaler:
#   Min size: 1   Max size: 2
```

Kapsule's autoscaler will provision the second DEV1-M when the next pod pressure event happens (e.g. when you scale a Deployment to 2 — step 2 below). For an immediate roll, manually scale the pool:

```bash
scw k8s pool update <pool-id> size=2
```

The new node picks up an IPv4 Flexible IP automatically — no extra config.

### Step 2 — restore replicas=2 on public-path deployments (no extra cost)

Once a second node exists, schedule a second replica of the gateway and mcp-server. CPU/memory requests are small (250m/256Mi total per pair) — both fit on the second DEV1-M.

```bash
kubectl scale deployment/gateway    --replicas=2 -n eurobase
kubectl scale deployment/mcp-server --replicas=2 -n eurobase
```

Update the manifests so the next deploy doesn't regress:

- `deploy/k8s/gateway.yaml` → `replicas: 2`
- `deploy/k8s/mcp-server.yaml` → `replicas: 2`
- `deploy/k8s/functions.yaml` → `replicas: 2` AND HPA `minReplicas: 2`

Commit + PR. The deploy script applies cleanly.

### Step 3 — switch to managed Redis (~€34-59/mo)

In-cluster Redis is fine indefinitely for **single-AZ** deploys. Switch to managed when you need:

- Cross-AZ realtime fan-out (you've added a second Kapsule pool in another zone)
- Durable rate-limit counters across node reboots
- Automated backups / monitoring
- Sub-millisecond SLAs

To switch:

1. Scaleway console → Databases → Redis → Create instance → RED1-micro (or the size you need)
2. Note the connection URL: `redis://default:<password>@<host>:6379`
3. Update the `REDIS_URL` value in the `eurobase-secrets` Kubernetes Secret:
   ```bash
   kubectl edit secret eurobase-secrets -n eurobase
   # Replace REDIS_URL value (it's base64-encoded — `echo -n "redis://..." | base64`)
   ```
4. Roll the gateway + worker so they pick up the new URL:
   ```bash
   kubectl rollout restart deployment/gateway deployment/worker -n eurobase
   ```
5. Verify gateway logs say `rate limiter connected to redis` and `realtime redis bridge connected`.
6. Once stable, remove the in-cluster Redis:
   ```bash
   kubectl delete -f deploy/k8s/redis.yaml
   ```
7. Update `deploy/create-secrets.sh` so a fresh environment bootstraps with the managed-Redis URL.

## Production → beta (cost-cutting)

The reverse direction — what we just did to get here. Documented so you can recover the savings on a quiet weekend.

### Step 1 — apply the in-cluster Redis manifest

```bash
kubectl apply -f deploy/k8s/redis.yaml
kubectl rollout status statefulset/redis -n eurobase --timeout=120s
```

Update the `REDIS_URL` Secret value to `redis://redis.eurobase.svc.cluster.local:6379` (no auth, internal-only):

```bash
NEW_URL=$(echo -n "redis://redis.eurobase.svc.cluster.local:6379" | base64)
kubectl patch secret eurobase-secrets -n eurobase \
  -p "{\"data\":{\"REDIS_URL\":\"$NEW_URL\"}}"
kubectl rollout restart deployment/gateway deployment/worker -n eurobase
```

Wait for gateway logs to show `rate limiter connected to redis`. Then in the Scaleway console → Databases → Redis → delete the managed RED1-micro instance.

### Step 2 — scale replicas down + pool to 1 node

```bash
kubectl scale deployment/gateway    --replicas=1 -n eurobase
kubectl scale deployment/mcp-server --replicas=1 -n eurobase
# Patch the HPA to allow min=1 (functions Deployment will follow)
kubectl patch hpa functions -n eurobase --type=merge -p '{"spec":{"minReplicas":1}}'

scw k8s pool update <pool-id> autoscaler.min-size=1 autoscaler.max-size=1
scw k8s pool update <pool-id> size=1
```

The autoscaler will drain and remove the second node within ~5 minutes. The freed IPv4 Flexible IP releases automatically (verify in Network → Public IPs that count is back to 1).

## Cost monitoring

Scaleway sends a monthly invoice. Two budget alerts worth setting in the console:

- **€50/mo** — anchor for beta. Anything above this needs explanation.
- **€150/mo** — anchor for production. Anything above suggests data-egress overage or autoscaler runaway.

Set via Scaleway console → Billing → Cost alerts.

## Things to never cut

- **Managed Postgres backups** (€0.06/mo). Trivial cost, every tenant's data is on it.
- **EU jurisdiction** (Scaleway fr-par). Moving to a US provider saves nothing and breaks the sovereignty pitch.
- **Audit log retention** in `public.audit_log`. Compliance pack is the Pro upsell.
