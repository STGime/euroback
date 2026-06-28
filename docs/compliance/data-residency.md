# Data Residency Hardening

> GDPR Art. 5(1)(f) (integrity & confidentiality), Art. 32 (security of
> processing), Schrems II / EDPB transfer-impact guidance, C5 (DE) and
> SecNumCloud (FR) baselines. Tier-1 #1 (#173).

How Eurobase's data-residency / encryption posture is enforced **in the
running system** — not in the design document — and how the customer-facing
DPA report reflects only what the runtime can prove.

## The three knobs

```
RESIDENCY_REGION      "France (Scaleway DC-PAR1 / DC-PAR2)"
ENCRYPTION_AT_REST    true
TLS_MIN               "TLS 1.3"
```

Set on the **gateway** Deployment (and any pod that renders the DPA
report — today that's the gateway only). Defaults match production, so a
missing env var renders the shipped posture. To **negate** a claim,
override explicitly: `TLS_MIN=""` makes the report say "in-transit
encryption: false". This is the truthfulness invariant — see
`internal/compliance/report.go` and `report_test.go::TestEncryptionInTransit_TiedToTLSMin`.

## What each knob asserts

### `RESIDENCY_REGION`

Human-readable jurisdiction shown in the DPA report's `data_flow.storage_location`.
Production: `"France (Scaleway DC-PAR1 / DC-PAR2)"`. Setting this to
anything other than an EU region is a sovereignty violation (CLAUDE.md
"No US cloud services permitted") and would block C5/SecNumCloud
attestation downstream.

### `ENCRYPTION_AT_REST`

The single operator-set boolean that confirms every customer-data
volume is encrypted at rest. The underlying truth on Scaleway is "always
yes":

| Volume        | Encryption                        | Source                                  |
| ------------- | --------------------------------- | --------------------------------------- |
| Postgres RDB  | AES-256-XTS volume encryption (Scaleway-managed) | Scaleway managed-DB docs |
| Object Storage| SSE-S3 (AES-256), every object    | Scaleway Object Storage docs            |
| Kapsule etcd  | AES-256-XTS volume + Secret encryption-provider-config (Scaleway-managed control plane) | Scaleway Kapsule docs |
| Redis         | Same managed-volume encryption    | Scaleway Managed Redis docs             |

> **Wording note.** Scaleway's public docs describe these volumes as
> "encrypted at rest" with the AES-256-XTS suite, without naming the
> specific Linux mechanism (LUKS vs dm-crypt direct vs in-kernel). The
> table above mirrors that wording exactly — overspecifying to "LUKS"
> would be an audit hazard if Scaleway's internals differ. If you need
> a tighter assertion (e.g. for a Schrems II TIA), request the
> mechanism in writing from Scaleway support and update the table.

These are non-toggleable on Scaleway, so the env var serves as an
**operator attestation**: the person deploying confirms they understand
the volume backing is encrypted. Set to `false` only when you actually
ship to a substrate that lacks at-rest encryption (custom self-hosted) —
and accept that the DPA report will then reflect that gap.

### `TLS_MIN`

The TLS floor enforced at the ingress controller, e.g. `"TLS 1.3"`.
The actual enforcement lives in **`deploy/k8s/nginx-ingress-config.yaml`**
— a controller-level `ConfigMap` setting `ssl-protocols: "TLSv1.3"` plus
HSTS preload. Setting `TLS_MIN` is a separate operator attestation that
the deployment is configured the way the report claims.

If `TLS_MIN=""` (empty), the DPA report's `encryption_in_transit` flag
flips to **false** rather than stay aspirationally `true`. This is the
single change that converts "the report says we're encrypted in transit"
from a polite fiction in dev/staging to a runtime-grounded fact.

## Sovereignty exception — DNS (Cloudflare)

CLAUDE.md states the sovereignty rule: "No US cloud services permitted
(AWS, GCP, Azure, Cloudflare, Stripe, Vercel)". DNS for `eurobase.app`
is **currently delegated to Cloudflare**, a US provider, as a temporary
operational measure: the apex zone has not yet been migrated to
Scaleway DNS. The cert-manager DNS-01 issuer in `deploy/k8s/ingress.yaml`
uses the Cloudflare resolver path for the same reason. The migration is
tracked in **#223**; the runbook for the swap is
`docs/runbooks/tls-cert-expiry.md`.

This exception is disclosed here so that the DPA report's sovereignty
posture can be reviewed in full. DNS metadata (zone queries, TXT writes
for ACME) flows through Cloudflare today; **no application data,
customer personal data, or backups** flow through Cloudflare. The
exception will close when #223 lands.

## Enforcement chain

The actual TLS 1.3 floor + HSTS are enforced at the **nginx-ingress
controller**, not on individual Ingress resources. Per-Ingress annotations
can't set `ssl-protocols` — it's a listener-level directive. The
controller reads it from a ConfigMap.

```
                                  ┌── enforced by ──┐
                                  ▼                 │
   client ──HTTPS──▶ nginx-ingress controller ──HTTP─▶ gateway pod
                          │
                          │  reads
                          ▼
              deploy/k8s/nginx-ingress-config.yaml
                  ssl-protocols: "TLSv1.3"
                  hsts: "true"
                  hsts-max-age: "31536000"
                  hsts-include-subdomains: "true"
                  hsts-preload: "true"
                  force-ssl-redirect: "true"
                  enable-ocsp: "true"
```

Apply via:

```bash
kubectl apply -n ingress-nginx -f deploy/k8s/nginx-ingress-config.yaml
```

Verify:

```bash
# TLS 1.3 only
testssl.sh --protocols api.eurobase.app
# HSTS header present + 1y max-age + preload
curl -sI https://api.eurobase.app | grep -i strict-transport-security
```

### HSTS lock-in (read before adding a subdomain)

`hsts-include-subdomains: "true"` + `hsts-preload: "true"` is a strong
one-way commitment: every current and future subdomain of
`eurobase.app` MUST serve HTTPS. A browser that has ever loaded
`api.eurobase.app` will refuse plaintext on **any** subdomain for the
HSTS lifetime (1 year, renewed on every visit).

Practical consequences:

- A marketing landing on `try.eurobase.app` cannot be HTTP-only.
- A status page, a Discord redirect, a temporary debug endpoint —
  none of them can serve plaintext.
- A misconfigured cert on any future subdomain breaks the browser
  trust path for that visit.
- We are **not** on the [HSTS preload list](https://hstspreload.org)
  today; we're just sending the header. Actually getting on the
  list requires explicit submission (the apex zone must also serve
  HSTS with `includeSubDomains` + `preload`, which today it does
  through the wildcard ingress). The header is preload-eligible,
  but absent submission no first-visit downgrade protection
  exists. Submitting to the list is a tracked follow-up.

Before adding a new subdomain, confirm the ingress will terminate TLS
for it. If you need an HTTP-only subdomain for any reason, the only
escape is to host it on a different apex zone (e.g.
`status.eurobase.dev`).

## Encryption-at-rest on Scaleway

See the table above. The `deploy/terraform/main.tf` resource tags
include `encryption-at-rest:luks-aes256xts` / `sse-s3-aes256` /
`etcd-encryption:scaleway-managed` so a `terraform state list` walk
gives an at-a-glance audit. The Scaleway provider does NOT expose
encryption as a queryable attribute (because it's non-toggleable), so
the tag is the closest thing to a state-side assertion.

## What the DPA report shows

After this PR, the `data_flow` section of `GET
/platform/projects/{id}/compliance/dpa-report` looks like:

```json
{
  "data_flow": {
    "storage_location": "France (Scaleway DC-PAR1 / DC-PAR2)",
    "encryption_at_rest": true,
    "encryption_in_transit": true,
    "tls_min": "TLS 1.3",
    "cross_border_transfers": false
  }
}
```

`tls_min` is a new field, surfaced because auditors ask for the floor
explicitly. If the env var is unset, the field is omitted
(`omitempty`) — clients should default to "unknown".

## Operator runbook

When deploying to a new environment:

1. Set the three env vars in the gateway Deployment.
2. Apply `deploy/k8s/nginx-ingress-config.yaml` into the
   `ingress-nginx` namespace.
3. Run `testssl.sh --protocols api.<env>` to verify TLS 1.3 only.
4. `curl -sI https://api.<env>/ | grep -i strict-transport-security`
   to verify HSTS.
5. Fetch `/platform/projects/<any-id>/compliance/dpa-report` and
   visually compare the `data_flow` fields with the verification
   commands above.

When a step would fail (e.g. you're shipping to a region without TLS
1.3 termination), **don't lie in the env vars** — set `TLS_MIN=""` and
let the report reflect reality. The whole point of #173 is that the
report is grep-able for what the runtime actually enforces, not what the
deployment doc aspired to.
