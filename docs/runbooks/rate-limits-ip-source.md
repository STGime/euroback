# Runbook: verifying the XFF chain before flipping `trust_proxy` defaults

## What this is for

Every per-project rate limit (signup/signin, token refresh, token verify, SMS) keys off "the client IP." How the gateway derives that IP is controlled by two knobs on `projects.auth_config.rate_limits`:

- `trust_proxy` (bool, default `false`) — when `true`, extract the client IP from `X-Forwarded-For`; when `false`, use the TCP peer.
- `trusted_proxy_hops` (int, default `1`) — how many hops between the internet and the gateway are trusted. Only consulted when `trust_proxy=true`. `ratelimit.ClientIPForProject` picks the XFF entry at index `len(entries) - trusted_proxy_hops` and treats anything to the left as caller-controlled + discards it.

Default is `false` because we haven't formalized what our LB + nginx-ingress chain actually writes to XFF in prod. This runbook is the check — run it once, capture the answer, and either flip the platform default (`DefaultRateLimits()` in `internal/tenant/auth_config.go`) to `true` with a matching `TrustedProxyHops`, or add specific ingress config as a hard precondition.

## Why the default stays `false` today

Not a safety concern — `ratelimit.ClientIPForProject` was hardened in PR #420 (`#238`) so that even under a hostile XFF (e.g. `use-forwarded-headers: true` on nginx that appends client-sent entries), the rightmost-hop extraction refuses caller-controlled values. The remaining question is a **correctness** one: does flipping the default actually produce meaningful per-user keys, or does the counter collapse to a per-cluster pseudo-key because SNAT hides real client IPs behind a small pool of LB/node addresses?

If it collapses, `trust_proxy=true` isn't more secure than `false` — it's just wrong. Under our current single-nginx-ingress chain and Scaleway LB defaults there's no observed operational win from flipping. This runbook is what changes that from "we assume" to "we verified."

## The check

Run from **inside** the cluster and from **outside**, so we see both sides of the LB + ingress.

### From inside the cluster

```sh
# Any pod with curl. If you have kubectl exec into any gateway pod:
POD=$(kubectl get pods -l app=eurobase-gateway -o name | head -1)
kubectl exec "$POD" -- curl -sS "https://api.eurobase.app/health" \
    -H 'X-Forwarded-For: 1.2.3.4, 5.6.7.8' \
    -H 'X-Test-Marker: rate-limits-ip-check' \
    -o /dev/null
```

Then check what the gateway recorded:

```sh
# Grep the gateway logs for this request. audit trail records client IP
# via ratelimit.ClientIPForProject with the *platform default* (trust=true,
# hops=1) — that value is what would be used if we flipped the tenant-level
# knob too.
kubectl logs -l app=eurobase-gateway --tail=200 | grep 'rate-limits-ip-check'
```

Also read `data_access_log` (the access-recorder audit output — see `internal/audit/access.go`) for the recorded IP field.

**Expected outputs (record actual):**

| Origin | Expected `X-Forwarded-For` at gateway | Expected `RemoteAddr` at gateway | What `ClientIPForProject(r, true, 1)` returns |
|---|---|---|---|
| Inside cluster (pod → svc → gateway) | client-sent XFF only (no LB append) | pod IP | leftmost-XFF `1.2.3.4` |
| Outside cluster (laptop → LB → nginx → gateway) | LB / ingress appended real client IP | nginx pod IP | rightmost-1 hop |

If the outside-cluster row's rightmost-1 hop **matches the laptop's public IP**, then `trust_proxy=true` + `trusted_proxy_hops=1` is safe to flip to default.

If it comes back as an LB / node IP, either `use-forwarded-headers` is not set on nginx (so nginx is writing the LB peer instead of the real client), or the LB is SNAT'ing without appending. **Do not flip the default until this is fixed.**

### From outside the cluster

```sh
# From a laptop with a known public IP.
# Public IP for reference:
curl -sS ifconfig.me; echo

# Then:
curl -sS "https://api.eurobase.app/health" \
    -H 'X-Test-Marker: rate-limits-ip-check-external' \
    -o /dev/null

# Grep the gateway log for that marker (from anywhere):
kubectl logs -l app=eurobase-gateway --tail=500 | grep 'rate-limits-ip-check-external'
```

Compare the recorded `client_ip` in the log line to the `ifconfig.me` output.

## What to change based on the answer

### If the recorded IP matches the real client IP

1. Flip `DefaultRateLimits().TrustProxy` to `true` in `internal/tenant/auth_config.go`. Keep `TrustedProxyHops = 1` (default) — matches the current single-nginx chain.
2. Update the console copy on the auth Rate Limits tab to match the new default.
3. Document the current ingress + LB config (Scaleway LB annotations, nginx-ingress ConfigMap) as a hard precondition here, so a future infra change can't silently break the guarantee.

### If the recorded IP is an LB / node IP

Fix the chain first. Two known-good shapes for nginx-ingress:

- **Preferred**: `compute-full-forwarded-for: true` + `use-forwarded-headers: true` + `proxy-real-ip-cidr: <Scaleway LB CIDR>` in the nginx-ingress ConfigMap. The controller then writes one authoritative XFF entry with the real client IP.
- **Alternative**: `externalTrafficPolicy: Local` on the ingress Service so the LB preserves the source IP + nginx doesn't SNAT. Requires the LB to be configured for PROXY protocol so it doesn't hairpin the connection.

Re-run the check after either change lands. Only then flip the default.

### If neither can be arranged today

Leave `trust_proxy = false`. Document that decision here so the "why isn't the default flipped yet?" question has a real answer next time it comes up.

## Related

- `internal/ratelimit/client_ip.go` — the extractor. See its doc-comment for the two failure modes (collapse vs. forgery) this runbook is trying to reason about.
- `internal/tenant/auth_config.go` — `DefaultRateLimits()`, `TrustProxy`, `TrustedProxyHops` fields on `RateLimits`.
- PR #420 — trusted-hop-count hardening (`#238`) that made this a correctness question rather than a security one.
- Console UI: Auth → Rate Limits → IP Address Forwarding.
