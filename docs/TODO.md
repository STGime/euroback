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
