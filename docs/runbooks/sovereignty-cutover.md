# Runbook: Sovereignty cutover — off Cloudflare & Google Cloud, onto Scaleway

## Why this runbook exists

`CLAUDE.md` sovereignty rule: **"No US cloud services permitted (AWS, GCP, Azure, Cloudflare, Stripe, Vercel)"**. Three residual US dependencies survive today on the marketing domain `eurobase.app`:

| Layer | Current | Target | Tracking issue |
|---|---|---|---|
| DNS zone + wildcard cert front | Cloudflare (US) | Scaleway DNS + Scaleway ACME | #223 |
| Marketing site hosting | Google Cloud Run `europe-west1` | Scaleway Serverless Containers `fr-par` | #503 |
| Email forwarding for aliases | Cloudflare Email Routing | Mailbox.org (DE) | #503 |

The three cannot be executed independently. Wrong order → user-visible downtime (visitors 5xx, mail bounces, TLS gap). This runbook is the zero-downtime playbook: four phases, each with an explicit rollback cheat.

## Hard constraints

1. **No visitor-facing 5xx window** on `eurobase.app` or `www.eurobase.app`.
2. **No lost inbound mail** to any advertised alias (`contact@`, `security@`, `dpo@`, `licensing@`, `hello@`, `press@`, `abuse@`).
3. **No wildcard-cert gap** on `*.eurobase.app` (the June-24 incident is precedent — see `tls-cert-expiry.md`).

## The insight that makes zero downtime possible

The marketing site is currently *proxied* through Cloudflare (`Server: cloudflare` on responses). The A record inside the CF zone points at Cloud Run. That means **the origin server can be swapped from inside the Cloudflare zone without any visitor-visible change** — CF terminates TLS with its own cert, forwards to whichever origin the A record names, visitor sees no transition.

Phase 2 exploits this: swap origin (Cloud Run → Scaleway) inside CF first. Only Phase 3 flips DNS away from CF, at which point our own cert-manager-issued cert takes over TLS.

---

## Phase 1 — Parallel run (invisible to users)

Stand up every new dependency. Nothing is cut over yet. Failure at any step here has zero customer impact.

### 1a. Provision Scaleway marketing origin

Recommendation: **Serverless Containers** (not Kapsule). The marketing site is a single static-nginx container; Serverless Containers is Scaleway's Cloud Run analogue, so it's a drop-in port. Kapsule adds no value and grows the surface to maintain.

- [ ] Create Scaleway Container Registry namespace: `rg.fr-par.scw.cloud/eurobase-marketing`.
- [ ] Add GitHub Actions workflow `.github/workflows/marketing-deploy.yml` in the `eurobase` repo — mirror of `cloudbuild.yaml` but pushing to Scaleway CR and deploying to Serverless Containers.
- [ ] Deploy to the Scaleway-issued endpoint (`<container-id>.fnc.fr-par.scw.cloud`). Do NOT wire it to `eurobase.app` yet.
- [ ] Smoke-test the Scaleway origin directly:
  ```sh
  curl -sI https://<container-id>.fnc.fr-par.scw.cloud/ | head -5
  curl -s https://<container-id>.fnc.fr-par.scw.cloud/ | grep -c '<title>Eurobase'
  ```
- [ ] **Attach custom domains `eurobase.app` + `www.eurobase.app` to the container** and wait for Scaleway's managed Let's Encrypt cert to issue for both names. This must be done **now**, in Phase 1, because after the Phase 3b NS flip these names resolve directly to the container (no CF cert in front any more) and the container must serve a valid cert for them from the first request. Verify:
  ```sh
  # From the Scaleway console, custom-domain rows show "Certificate: valid".
  # From the CLI, hit the container hostname with the target Host header:
  curl -sI --resolve eurobase.app:443:$(dig +short <container-id>.fnc.fr-par.scw.cloud | tail -1) \
    https://eurobase.app/ | grep -iE 'HTTP|server'
  # Expect 200 + Server: nginx (not CF), served over TLS with a cert whose SAN
  # includes eurobase.app + www.eurobase.app.
  ```
- [ ] Note: **Serverless Containers have no stable IP.** They expose a `*.scw.cloud` hostname and route incoming traffic by SNI/Host header. Downstream steps (Phase 2a, Phase 3) must use a CNAME (or CF's CNAME-flattening at apex), never an A record.

### 1b. Provision Mailbox.org tenancy

- [ ] Create the Mailbox.org account under `eurobase.app` (Team plan, sufficient for six aliases + one destination mailbox).
- [ ] Configure each alias to forward to the current destination mailbox:
  - `contact@`, `security@`, `dpo@`, `licensing@`, `hello@`, `press@`, `abuse@`
- [ ] Record Mailbox.org's MX hosts, SPF include, DKIM public keys, and DMARC recommendation.
- [ ] Do NOT change any DNS yet — Mailbox.org sits idle waiting for MX flip in Phase 2.

### 1c. Mirror DNS records into a Scaleway DNS zone

- [ ] Create zone `eurobase.app` on Scaleway DNS.
- [ ] Mirror every record from the current Cloudflare zone (A, AAAA, CNAME, MX, TXT, CAA). Keep any `_acme-challenge` automation config the Scaleway ACME issuer will need.
- [ ] **CAA sanity-check** — the mirrored CAA record must authorize Let's Encrypt for *both* issuance paths that will be active post-cutover: (a) cert-manager's DNS-01 wildcard for `*.eurobase.app` and (b) the Scaleway-managed cert for the Serverless Container's `eurobase.app` + `www.eurobase.app` custom domains. Simplest safe value: `0 issue "letsencrypt.org"` (covers both). Any tighter policy (`issuewild` restrictions, account-URI pinning) must be reviewed before Phase 3 or Scaleway's managed cert renewal will silently fail once CF's CA is out of the path.
- [ ] Do NOT change registrar NS records yet. Scaleway zone is dormant until Phase 3.
- [ ] Verify parity:
  ```sh
  # Dump both zones, sort, diff.
  gh api / ... # export from CF (or use `flarectl` / `octodns-sync`)
  scw dns record list eurobase.app > /tmp/scw-zone.txt
  diff <(sort /tmp/cf-zone.txt) <(sort /tmp/scw-zone.txt)
  ```
  Expect zero substantive differences — only the SOA/NS records legitimately differ.

**Phase 1 checkpoint.** All new origins are live and verified in isolation. No visitor traffic has moved.

---

## Phase 2 — Staged cutover (all changes inside the existing Cloudflare zone)

Every change in Phase 2 happens in the Cloudflare control panel or via CF API. NS records untouched — visitors still resolve through Cloudflare, so the failure blast radius is one record edit away from rollback.

### 2a. Repoint the marketing origin → Scaleway Serverless Container

Serverless Containers route by SNI/Host header and have no stable IP, so this is a **CNAME**, not an A record. At the apex, use Cloudflare's CNAME-flattening (which returns the resolved A record to clients at query time — the record type inside CF is CNAME, on the wire it's an A). `www` is a straight CNAME.

- [ ] Lower TTL to 300s in Cloudflare on both `eurobase.app` (apex, CNAME-flattened) and `www.eurobase.app` (CNAME) 24 hours before the swap.
- [ ] Update both records to target the Serverless Container hostname (`<container-id>.fnc.fr-par.scw.cloud`). **Keep CF proxy status "orange-cloud"** — CF stays as the TLS termination + edge cache during Phase 2.
- [ ] Verify against the direct Scaleway origin (bypassing CF), using hostname resolution — **not** `--resolve :<ip>`, because a bare IP hit doesn't carry the container's SNI:
  ```sh
  curl -sI --resolve eurobase.app:443:$(dig +short <container-id>.fnc.fr-par.scw.cloud | tail -1) \
    https://eurobase.app/ | head -5
  # Or the simpler indirect check: hit the container hostname with a Host override.
  curl -sI -H 'Host: eurobase.app' https://<container-id>.fnc.fr-par.scw.cloud/ | head -5
  ```
- [ ] Verify through CF (visitor perspective):
  ```sh
  curl -sI https://eurobase.app/ | grep -iE 'server|cf-ray|status'
  # Expect: Server: cloudflare (still), 200 OK, CF-Ray present.
  ```
- [ ] Monitor CF Analytics for 5xx rate — should stay near zero.

**Rollback:** revert the CNAME target to the Cloud Run hostname. TTL is 300s → full recovery in ≤5 min.

### 2b. Swap MX records → Mailbox.org

- [ ] Lower MX TTL to 300s in Cloudflare 24 hours ahead.
- [ ] Update MX records → Mailbox.org's MX hosts.
- [ ] Add Mailbox.org's SPF include, DKIM records, DMARC policy (start at `p=none` for observation, tighten to `quarantine` in Phase 4).
- [ ] **Keep Cloudflare Email Routing rules active for 72 hours.** During DNS propagation some resolvers still return the old MX; CF's rules will catch those. As long as both CF and Mailbox.org forward to the same destination mailbox, no mail is lost.
- [ ] Verify inbound:
  ```sh
  # From an external mailbox, send one message to each alias.
  # Confirm each arrives at the destination mailbox exactly once.
  ```

**Rollback:** revert MX to CF's mail routing hosts; CF rules already active, so mail flow is restored immediately.

### 2c. 72-hour soak

- [ ] Watch CF 5xx rate + Scaleway container error logs + Mailbox.org inbound counts for 72 hours.
- [ ] Do NOT proceed to Phase 3 until all three metrics are quiet.

**Phase 2 checkpoint.** Marketing site is served from Scaleway (still fronted by CF). Mail flows through Mailbox.org (CF as safety net). No visitor-visible change. Cloud Run and CF Email Routing are now hot spares.

---

## Phase 3 — DNS + cert flip

This is the only phase with real customer-visible risk. The NS change at the registrar propagates over 24–72h and cannot be atomic. Cert issuer swap must land in the same window so the next renewal succeeds against the new authoritative DNS.

### 3a. Pre-flight

Two independent cert stories must both be healthy before the NS flip — one for the Kapsule ingress hostnames (`api`/`console`/`mcp`/`*.eurobase.app`), one for the marketing apex + `www` (Serverless Container managed cert). If either is not ready, do not flip NS.

- [ ] Verify the dormant Scaleway `ClusterIssuer` and `scaleway-credentials` Secret are healthy (they've been declared but idle since #220):
  ```sh
  kubectl -n eurobase get clusterissuer letsencrypt-prod-dns -o yaml \
    | yq .status
  kubectl -n cert-manager get secret scaleway-credentials -o yaml \
    | yq '.data | keys'
  ```
- [ ] **Re-verify the Serverless Container's managed cert for `eurobase.app` + `www.eurobase.app`** (provisioned in Phase 1a) — Scaleway auto-renews these, but check the console dashboard's "Certificate: valid" line and `notAfter` explicitly. The wildcard `*.eurobase.app` does NOT cover the bare apex; the Serverless Container's own cert is the only thing keeping the apex + `www` TLS-valid the moment DNS resolves off Cloudflare.
- [ ] Confirm the Scaleway zone (from Phase 1c) is complete and current — one more diff run.
- [ ] Note: the current wildcard cert (from the CF-solver era, covering `*.eurobase.app` on Kapsule) stays valid until its `notAfter`. Renewal need not happen in this window; the Phase 3c issuer swap is for the *next* renewal.

### 3b. Registrar NS swap

- [ ] Lower CF zone SOA TTL to 300s 48 hours before the NS change (registrar itself controls the delegation TTL; that's usually 48h and NOT under our control — plan accordingly).
- [ ] At the registrar, replace CF nameservers with Scaleway nameservers.
- [ ] Watch propagation:
  ```sh
  # Repeat every 30 min from several vantage points until only Scaleway NS returned.
  dig +short NS eurobase.app @1.1.1.1
  dig +short NS eurobase.app @8.8.8.8
  dig +short NS eurobase.app @9.9.9.9
  ```
- [ ] During propagation, both CF and Scaleway zones are authoritative for different resolvers. Both zones must remain in sync (they are — Phase 1c mirrored). Do NOT edit either zone during this window.

### 3c. Cert-manager issuer swap

- [ ] Edit `deploy/k8s/ingress.yaml`: `Certificate.spec.issuerRef.name: letsencrypt-prod-dns-cloudflare` → `letsencrypt-prod-dns`.
- [ ] Apply and force a fresh issuance to prove the Scaleway solver works before the current cert expires:
  ```sh
  kubectl -n eurobase apply -f deploy/k8s/ingress.yaml
  kubectl -n eurobase annotate certificate eurobase-wildcard \
    cert-manager.io/issue-temporary-certificate=true --overwrite
  kubectl -n eurobase delete secret eurobase-wildcard-tls  # forces renewal
  ```
  Or simply annotate + wait 24h for cert-manager to run its own renewal check.
- [ ] Verify via the existing monitor:
  ```sh
  scripts/ops/cert-status.sh
  # Expect exit 0; notAfter is a new date (~90 days out).
  ```

**Rollback:** at the registrar, restore CF nameservers. Scaleway zone stays valid; CF zone is still populated (only Phase 4 removes it). Cert issuerRef reverts in Git.

---

## Phase 4 — Decommission

Only after Phase 3 has soaked cleanly for 7 days.

- [ ] Remove Cloudflare Email Routing rules (mail already served by Mailbox.org).
- [ ] Delete the Cloudflare zone (or leave it dormant — some registrars retain it as a courtesy).
- [ ] Tighten DMARC from `p=none` → `p=quarantine` (then observe two weeks) → `p=reject`.
- [ ] Delete the Cloud Run service, the Artifact Registry repo, and — once the Google Cloud project has no other footprint — the project itself.
- [ ] Remove the `cloudflare-api-token` Secret from `cert-manager` namespace; remove the `letsencrypt-prod-dns-cloudflare` ClusterIssuer.
- [ ] Update `deploy/k8s/ingress.yaml` header comment, `docs/runbooks/tls-cert-expiry.md`, and `CLAUDE.md` to drop the "Cloudflare exception" language.
- [ ] Close #503, then close #223.
- [ ] Announce internally + update the GTM tracker's sovereignty workstream status.

## Rollback matrix

| Failure at | Time-to-restore | Action |
|---|---|---|
| Phase 1 (any) | Immediate — nothing was cut over | Delete the failing new resource; iterate |
| Phase 2a (marketing origin) | ≤5 min at TTL | Revert A record in CF |
| Phase 2b (MX) | ≤5 min at TTL | Revert MX; CF Email Routing catches during backfill |
| Phase 3b (NS) | 24–48h (worst-case a full TTL cycle at the registry) | Restore NS at registrar; Scaleway zone stays live in parallel — no data lost, only slow to fail-back |
| Phase 3c (cert issuer) | Next renewal cycle | Revert `issuerRef` in Git; CF issuer + Secret still present until Phase 4 |
| Phase 4 (any) | Undo the specific decommission action; both stacks were still live before this phase |

## Comms

- Announce Phase 2 and Phase 3 dates in the beta users' Discord `#announcements` channel 48h ahead. Phrase as "sovereignty cleanup" not "migration risk" — the plan is zero-downtime.
- Post the completion of Phase 4 to the marketing blog: an update post that celebrates the sovereignty story now being fully clean (matches the tone of `2026-08-27-public-beta-open.md`).

## Refs

- #223 — DNS zone migration off Cloudflare
- #503 — marketing hosting + email forwarding migration
- #220 — TLS cert monitor + dormant Scaleway ACME issuer (already in place for Phase 3c)
- `docs/runbooks/tls-cert-expiry.md` — the outage of record that this plan avoids repeating
- `CLAUDE.md` → Sovereignty section
