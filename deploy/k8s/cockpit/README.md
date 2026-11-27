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

## Service logs (`alloy-logs.yaml`)

Pod logs only live on the node while the pod exists, so a deploy loses the
previous pods' lines. `alloy-logs.yaml` keeps them in Cockpit for 7 days.
It is separate from the metrics pipeline above and runs without it.

```
pods in eurobase + ingress-nginx      alloy-logs pod                       Scaleway Cockpit
┌──────────────────────┐  Kubernetes  ┌────────────────────────┐  HTTPS  ┌──────────────────┐
│ gateway, worker,     │ ───── API ──►│ read → mask → push     │ ──────► │ Logs data source │
│ functions, poolers,  │  (pods/log)  │ (emails, credentials)  │         │ (Loki), 7 days   │
│ console, mcp, ingress│              └────────────────────────┘         └──────────────────┘
└──────────────────────┘
```

- **What:** every container's stdout/stderr in `eurobase` and `ingress-nginx`
  (ingress access lines include client IPs), lines cut at 16 KiB, **except
  customer function output**: from the functions runner only lines with the
  runner's own prefixes (`[runner]`, `[tenant-db]`, `[vault]`, `[fn] `,
  startup / request-rejection / `FATAL` lines, Deno's `error:` / `Warning`
  lines, Deno-format stack frames, and `[fn:<project>] Worker error` /
  `vault.get failed`) are sent, at most 20 lines/s (burst 200). A Worker
  error keeps the error class; its message (the tenant's text) is replaced.
  What tenant code prints (`console.log`, the `[fn:<project>] LEVEL:` mirror
  of `ctx.log`) stays in `edge_function_logs`. Tenant code shares the
  runner's stdout, so it can print a line that starts with one of those
  prefixes: read runner lines in Cockpit as hints, not proof.
- **Labels:** `namespace`, `app`, `container`, `level` (only `DEBUG` / `INFO`
  / `WARN` / `ERROR` from slog's JSON `level`), plus `job` and `instance`
  (`namespace/pod:container`) added by `loki.source.kubernetes`. Project ids,
  user ids and emails stay in the line, never a label.
- **Masked before sending:** email addresses, also non-ASCII and
  URL-encoded (`a****@example.com`, like `email.MaskEmail`); phone numbers in
  international format (`+46****67`); paths and query strings of full URLs
  (`https://host/[redacted]`); passwords in connection strings; JWTs;
  `eb_pk_` / `eb_sk_` / `eb_pat_` / `eb_ptk_` keys and `whsec_` secrets;
  `Bearer …` / `Basic …`; `X-Api-Key`; values of `apikey`, `token`,
  `access_token`, `refresh_token`, `id_token`, `token_hash`, `code_verifier`,
  `password`, `client_secret`, `jwt_secret`, `webhook_secret`,
  `signing_secret` as JSON fields (also escaped inside a JSON string) or
  `key=value`; query parameters `code`, `otp`, `secret`, `sig`, `signature`,
  `X-Amz-Signature`, `X-Amz-Credential`. A value containing an escaped quote
  is only masked up to that quote.
- **Restarts:** read positions are kept in an emptyDir. When the collector
  pod is recreated (first apply, config change, node drain) it re-sends every
  container's retained log once (kubelet keeps ~10Mi per container), so
  expect duplicates around that time (Loki drops exact repeats on the same
  stream, and refuses entries older than its accepted window with 400s in
  the collector's log). App deploys don't cause this.
- **Cockpit unreachable:** `loki.write` retries with backoff for a few
  minutes, then drops the batch; reading pauses meanwhile, so memory stays
  bounded. A long outage loses lines that kubelet rotates away.
- **Access:** read pods and `pods/log` in the two namespaces (Roles, no
  ClusterRole); no ingress; egress to DNS and to TCP 443 / 6443 on any
  address (the Cockpit push endpoint and the API server).
- **Cost:** custom logs are billed per GB ingested (we produce well under
  0.1 GB a month); 7 days of retention are free.

### Setup

1. **Cockpit → Data sources → Create** a custom data source of type *Logs*,
   region fr-par, retention **7 days**. Note its *API URL*.
2. **Cockpit → Tokens → Create** a token in fr-par that can only push logs.
   It is shown once.
3. Create the secret (the token never goes into `eurobase-secrets`):
   ```bash
   kubectl create secret generic cockpit-logs-credentials -n eurobase \
     --from-literal=url='<API URL>/loki/api/v1/push' \
     --from-literal=token='<token>'
   ```
4. Apply and check:
   ```bash
   kubectl apply -f deploy/k8s/cockpit/alloy-logs.yaml
   kubectl -n eurobase rollout status deploy/alloy-logs
   kubectl -n eurobase logs deploy/alloy-logs --tail=50   # no push errors (401/403)
   ```
   On 401/403, switch `tenant_id` to `bearer_token` in the `loki.write`
   block (comment in the config) and re-apply.
5. In Cockpit's Grafana, **Explore → the logs data source**:
   `{namespace="eurobase", app="gateway"}` shows the gateway's lines within a
   minute. Check that emails appear masked, then roll the gateway
   (`kubectl -n eurobase rollout restart deploy/gateway`) and confirm the old
   pod's lines are still there.

Useful queries: `{app="functions"} |= "[runner] BUG"`,
`{namespace="eurobase", level="ERROR"}`, `{app="functions"} |= "[tenant-db]"`.

**Rollback:** `kubectl delete -f deploy/k8s/cockpit/alloy-logs.yaml` (the
apps are unaffected; they only write to stdout). Lines already in Cockpit
expire with the retention.

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
