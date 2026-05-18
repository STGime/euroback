# TODO

## Scaleway Infrastructure — Wildcard Subdomain Setup ✅ DONE (verified 2026-05-18)

SDK URLs (`{slug}.eurobase.app`) resolve in production.

- [x] Scaleway cert-manager webhook installed (cert-manager ns active)
- [x] `scaleway-credentials` secret present in cert-manager namespace
- [x] Wildcard DNS `*.eurobase.app` → `163.172.128.215` (ingress-nginx LB)
- [x] `deploy/k8s/ingress.yaml` applied — wildcard rule + DNS-01 ClusterIssuer + Certificate
- [x] `kubectl get certificate eurobase-wildcard -n eurobase` → Ready=True (valid until 2026-06-24)
- [x] Gateway running with `SubdomainMiddleware`; `DOMAIN_SUFFIX` defaults to `eurobase.app`
- [x] Smoke: real slug resolves (401 missing apikey), unknown slug returns `{"error":"project not found"}`

## Scaleway TEM — Transactional Email (Pre-Production)

DNS & domain verification (required before TEM can send):

- [ ] Add SPF record for `eurobase.app`: `v=spf1 include:_spf.scw-tem.cloud -all`
- [ ] Configure DKIM via Scaleway TEM console — add the generated CNAME/TXT records to `eurobase.app`
- [ ] Add DMARC record: `_dmarc.eurobase.app TXT "v=DMARC1; p=quarantine; rua=mailto:dmarc@eurobase.app"`
- [ ] Verify domain ownership in Scaleway TEM console (status = verified)

Environment variables (add to gateway deployment / k8s secret):

- [ ] `SCW_TEM_SECRET_KEY` — Scaleway API secret key with TEM permissions
- [ ] `SCW_TEM_REGION=fr-par` — TEM region
- [ ] `EMAIL_FROM_ADDRESS=noreply@eurobase.app` — sender address (must match verified domain)
- [ ] `EMAIL_FROM_NAME=Eurobase` — sender display name
- [ ] `CONSOLE_URL=https://console.eurobase.app` — used in email action links (reset/verify URLs)

Database migration:

- [ ] Run migration 000015: `migrate -path migrations -database "$DATABASE_URL" up`
- [ ] Verify: `\dt public.platform_email_tokens` and `\dt public.email_templates` exist
- [ ] Verify: existing tenant schemas have `email_tokens` table (backfill ran)

Smoke tests (after deploy):

- [ ] Gateway starts without crash when `SCW_TEM_SECRET_KEY` is empty (graceful degradation)
- [ ] `GET /platform/config/email-status` returns `{"configured": true}` with TEM env vars set
- [ ] `POST /platform/auth/forgot-password` returns 200 (no crash, email sent or logged)
- [ ] `POST /v1/auth/forgot-password` with API key returns 200
- [ ] Console login page shows "Forgot password?" link
- [ ] Console auth settings page loads Email Templates tab
- [ ] Send a test email from the Email Templates tab — verify delivery

Rate limiting (verify in Redis):

- [ ] Forgot-password: max 3 per email per 15 min (not yet rate-limited — add if Redis available)
- [ ] Resend-verification: max 1 per email per 5 min (not yet rate-limited — add if Redis available)
