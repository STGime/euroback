# TODO

## Scaleway Infrastructure — Wildcard Subdomain Setup

These steps are required to make SDK URLs (`{slug}.eurobase.app`) work in production.

- [ ] Install [Scaleway cert-manager webhook](https://github.com/scaleway/cert-manager-webhook-scaleway) in the Kapsule cluster
- [ ] Create `scaleway-credentials` secret in cert-manager namespace with `SCW_ACCESS_KEY` and `SCW_SECRET_KEY`
- [ ] Add wildcard DNS record: `*.eurobase.app` → ingress load balancer IP (A or CNAME)
- [ ] Deploy updated `deploy/k8s/ingress.yaml` (wildcard rule + DNS-01 issuer + Certificate resource)
- [ ] Verify wildcard TLS cert is issued: `kubectl get certificate eurobase-wildcard -n eurobase`
- [ ] Deploy updated gateway (includes `SubdomainMiddleware`)
- [ ] Set `DOMAIN_SUFFIX=eurobase.app` in gateway deployment env (or omit — it's the default)
- [ ] Smoke test: `curl -H "apikey: eb_pk_..." https://{slug}.eurobase.app/v1/db/{table}`

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
