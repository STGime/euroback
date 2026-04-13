# Scaleway Cockpit Monitoring

Metrics pipeline for the Eurobase gateway. Stays fully inside Scaleway fr-par
(Paris) — no US infrastructure in the data path.

## Architecture

```
gateway pod                        alloy pod
┌──────────────────┐   scrape     ┌──────────────┐   remote_write   ┌─────────────────────┐
│ :8080  public    │              │ Grafana      │ ───── HTTPS ───► │ Scaleway Cockpit    │
│ :9100  /metrics  │ ◄─── pull ── │ Alloy        │                  │ (Prometheus+Grafana)│
└──────────────────┘   ClusterIP  └──────────────┘                  │  fr-par, managed    │
                                                                    └─────────────────────┘
```

- **Gateway** exposes `/metrics` on port 9100, bound only to the cluster-internal
  `gateway` Service. The public ingress never routes to 9100.
- **Grafana Alloy** (open source, Apache-2.0) runs in-cluster, discovers pods by
  the `prometheus.io/scrape=true` annotation, and remote-writes to Cockpit.
- **Scaleway Cockpit** stores metrics, hosts Grafana dashboards, and runs alert
  rules. Free tier is generous enough for MVP traffic.

## Setup

### 1. Create a Cockpit project and push token

In the Scaleway console:

1. **Cockpit → Data sources → Metrics** → note the *Push URL*
   (typically `https://<token-id>.metrics.cockpit.fr-par.scw.cloud/api/v1/push`).
2. **Cockpit → Tokens → Generate** with scope `Push metrics`. Copy the token
   once — it is shown only at creation time.

### 2. Create the in-cluster secret

```bash
kubectl create secret generic cockpit-credentials \
  --namespace eurobase \
  --from-literal=url='https://<your-push-url>/api/v1/push' \
  --from-literal=token='<push-token>'
```

### 3. Deploy Alloy

```bash
kubectl apply -f deploy/k8s/cockpit/alloy.yaml
kubectl -n eurobase rollout status deploy/alloy
kubectl -n eurobase logs -l app=alloy --tail=50
```

You should see Alloy discover the gateway pods and start scraping every 30s.

### 4. Redeploy the gateway with the metrics port

The updated `deploy/k8s/gateway.yaml` adds `containerPort: 9100` and the
`prometheus.io/scrape` annotation Alloy looks for.

```bash
kubectl apply -f deploy/k8s/gateway.yaml
```

### 5. Verify end-to-end

```bash
# Local proof: hit /metrics via port-forward.
kubectl -n eurobase port-forward svc/gateway 9100:9100
curl -s localhost:9100/metrics | grep eurobase_

# In Cockpit's Grafana UI, open Explore → metric name starts with `eurobase_`.
# The Postman collection in docs/eurobase-monitoring.postman_collection.json
# exercises the same endpoints with response-format assertions.
```

### 6. Load alert rules

Import `deploy/k8s/cockpit/alerts.yaml` into Cockpit Grafana:

- **UI**: Alerting → Alert rules → New rule → Import YAML, select
  datasource `cockpit`.
- **API** (preferred for GitOps):
  ```bash
  curl -X POST "$GRAFANA_URL/api/ruler/grafana/api/v1/rules/eurobase" \
    -H "Authorization: Bearer $GRAFANA_API_TOKEN" \
    -H "Content-Type: application/yaml" \
    --data-binary @deploy/k8s/cockpit/alerts.yaml
  ```
- **Terraform**: use the `grafana_rule_group` resource
  (provider `grafana/grafana`, EU endpoint).

Configure a contact point (email via Scaleway TEM is sufficient for MVP; add
PagerDuty-EU or a self-hosted alternative later if you need escalation).

## Why a separate metrics port?

The `/metrics` output leaks information that is safe internally but risky to
expose publicly: per-route latency distributions, goroutine counts, process
memory, panic counters. Binding it to port 9100, which the ingress never
routes to, keeps it reachable only from within the cluster VPC.

## Sovereignty notes

| Component | Hosted at | EU-only? |
|-----------|-----------|----------|
| Gateway `/metrics` | Scaleway Kapsule (fr-par) | ✅ |
| Grafana Alloy | Scaleway Kapsule (fr-par) | ✅ |
| Scaleway Cockpit (Prometheus+Grafana) | Scaleway (fr-par) | ✅ |
| Alert notifications via Scaleway TEM | Scaleway (fr-par) | ✅ |

Avoid: Grafana Cloud (US datacenter by default — choose EU region only if
mandatory), Datadog, New Relic, Sentry SaaS.
