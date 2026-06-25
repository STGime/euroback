# Runbook: TLS certificate expiry on `*.eurobase.app`

## When this fires

The `tls-cert-monitor` CronJob (`deploy/k8s/cert-monitor-cronjob.yaml`) posts
to Discord `#alerts` in two flavours:

- **`⚠️ Eurobase TLS — certificate expiring soon`** — at least one host's
  certificate is < `WARN_DAYS` (default 14) from expiry but is still valid
  today. No customer impact yet.
- **`🚨 Eurobase TLS — expired or unreachable`** — at least one host's
  certificate has already expired, or the TLS handshake failed entirely.
  Every tenant subdomain on the wildcard refuses TLS. `@everyone`.

The same code path is in `scripts/ops/cert-status.sh` — runnable from a
laptop or CI to reproduce what the CronJob saw.

## What broke (incident of record: 2026-06-24)

Pure precedent for this runbook. Wildcard `*.eurobase.app` was issued on
**2026-03-26** with a 90-day Let's Encrypt lifetime → expired
**2026-06-24 at 11:11 UTC**. cert-manager's `eurobase-wildcard` Certificate
condition stayed `Renewing` from May 25 onward but the underlying ACME
challenge was stuck `pending` for 30 days — because the DNS-01 webhook
referenced in `ingress.yaml` was the Scaleway webhook, but
**`eurobase.app` is hosted on Cloudflare DNS, not Scaleway**, so every
attempt to write the `_acme-challenge` TXT record returned `403: domain
not found`. cert-manager logged 628+ `PresentError` warnings against an
unwatched log stream.

Customer impact: ~50 minutes of `*.eurobase.app` TLS-down.

## Recovery — order of operations

The fix path used in the June 24 incident.

### 1. Confirm the failure mode

```sh
# What does a public client see right now?
echo | openssl s_client -servername livestylist.eurobase.app \
  -connect livestylist.eurobase.app:443 2>/dev/null \
  | openssl x509 -noout -dates -subject -issuer
```

If `notAfter` is in the past or the command fails to print a cert: yes,
we're down.

### 2. Find which renewal pipeline is broken

```sh
kubectl -n eurobase get certificate eurobase-wildcard -o yaml | yq .status
kubectl -n eurobase get certificaterequests
kubectl -n eurobase get orders,challenges
```

A `Certificate` condition `Issuing=True` for > 24 hours with a `pending`
`Challenge` of the same age is the standard pattern.

```sh
# Get the actual rejection from the DNS solver:
kubectl -n eurobase describe challenge <name> | grep -E "Reason:|Events:"
```

Typical reasons we've seen:

| Reason | What it means | Fix |
|---|---|---|
| `failed to update DNS zone recrds: 403 Forbidden: domain not found` | The configured DNS solver is pointed at a provider that doesn't host this zone. | Swap `Certificate`'s `issuerRef` to a `ClusterIssuer` whose solver matches the real authoritative DNS. |
| `secrets "<creds>" not found` | The cert-manager namespace is missing the API-key Secret the solver needs. | Create the Secret (DNS-write-only scoped key) and re-create the failed CR. |
| `presented: false` with no events on the new Challenge | Old stuck Challenges (parent Order already gone) hold finalizers and re-enter the controller's queue forever, starving new Challenges. | `kubectl -n eurobase patch challenge <old> --type=merge -p '{"metadata":{"finalizers":[]}}'`. |

### 3. Verify the authoritative DNS provider

```sh
dig +short NS eurobase.app
# harlee.ns.cloudflare.com / ian.ns.cloudflare.com → Cloudflare
```

Match the cert-manager `ClusterIssuer` to that provider. `ingress.yaml`'s
`letsencrypt-prod-dns-cloudflare` uses cert-manager's native Cloudflare
solver and an API token Secret named `cloudflare-api-token` in the
`cert-manager` namespace.

> **Sovereignty note (CLAUDE.md):** Cloudflare is on the "no US cloud
> services" list. The Cloudflare DNS solver is a deliberate temporary
> exception until the apex `eurobase.app` zone migrates to Scaleway DNS.
> Once that happens, the Scaleway webhook (already installed under
> `cert-manager-webhook-scaleway` Helm release) becomes the active
> issuer and the Cloudflare one is removed.

### 4. Force a fresh issuance

After the solver path is healthy:

```sh
# Triggers cert-manager to create a new CertificateRequest → Order → Challenge.
kubectl -n eurobase delete certificaterequest \
  -l cert-manager.io/certificate-name=eurobase-wildcard

kubectl -n eurobase get certificate eurobase-wildcard -w
# (Wait for READY=True; typically 30-90s once the solver is healthy.)
```

If stuck Challenges from previous attempts haven't been reaped, strip
their finalizers (see table above). The controller is single-threaded
per Challenge and a finalizer loop on an orphan starves all new work.

### 5. Validate end-to-end

```sh
./scripts/ops/cert-status.sh
# expect all OK, all notAfter ~90 days out.

# Plus the downstream symptom the user reported:
curl -fsS https://api.eurobase.app/health
```

## Why monitoring catches this next time

`scripts/ops/cert-status.sh` and `deploy/k8s/cert-monitor-cronjob.yaml`
both check the **served** certificate via `openssl s_client`, not
cert-manager's internal `Certificate.status`. That distinction matters:
the June 24 outage had `Certificate.status.conditions[Ready]=True` while
the Secret in the cluster had never been updated past the original
issuance. An internal-state alert would not have fired; a host-truth
alert would have, 14 days before expiry. The CronJob runs hourly; the
script is the same logic for local + CI reproduction.

## Standing exception: the legacy Scaleway issuer

`ingress.yaml` still defines `ClusterIssuer letsencrypt-prod-dns`
pointing at the Scaleway webhook — kept as a no-op placeholder so the
migration to Scaleway DNS (when it lands) is a one-line `issuerRef`
swap on the `Certificate`, not a manifest rewrite. Until then it has
no live `Certificate` consumers; `eurobase-wildcard` references
`letsencrypt-prod-dns-cloudflare`. The verification step in section 2
above always names the issuer explicitly so the wrong one cannot be
picked up by accident.
