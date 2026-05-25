# Runbook — Mollie billing

Integration shipped under issues #157–#162 (umbrella #2). Mollie is
the only billing provider — Netherlands-based, listed in the DPA
sub-processor registry since migration 000025, CLAUDE.md-compliant
("no US cloud services").

This runbook covers first-time setup, day-to-day operator tasks, and
the failure modes you'll see in production.

## First-time setup

### 1. Create Mollie account + organisation

- Sign up at https://www.mollie.com/dashboard (one Mollie organisation
  per Eurobase environment — separate orgs for staging and prod, OR
  one org with separate profiles for each)
- Enable the Eurobase profile in the dashboard
- Verify your business details so Mollie unblocks live mode

### 2. Generate API keys

In the Mollie dashboard → Developers → API keys:

- Copy the **test key** (`test_*`) for dev/staging environments
- Copy the **live key** (`live_*`) for production
- Generate a webhook signing secret (32+ random bytes,
  `openssl rand -hex 32`) and save it somewhere you control —
  Mollie does NOT generate this for you; we use it client-side
  to verify webhook payloads

### 3. Populate vault keys

The gateway reads two env vars at boot:

| Env var | Where |
|---|---|
| `MOLLIE_API_KEY` | `eurobase-secrets` k8s Secret, key `MOLLIE_API_KEY` |
| `MOLLIE_WEBHOOK_SECRET` | same Secret, key `MOLLIE_WEBHOOK_SECRET` |

Optional:

| Env var | Default | Purpose |
|---|---|---|
| `PUBLIC_GATEWAY_URL` | `https://api.eurobase.app` | Used to build the webhook URL Mollie POSTs back to |
| `CONSOLE_URL` | `http://localhost:5173` | User's browser returns here after Mollie checkout |

The gateway logs `billing: Mollie not configured` if either of the
two required vars is missing — every `/billing` endpoint then returns
503 (fail-closed, no silent no-ops).

### 4. Configure Mollie webhook URL

In the Mollie dashboard → Developers → Webhooks, set the URL to:

```
https://api.eurobase.app/webhooks/mollie
```

(or your environment's equivalent of `$PUBLIC_GATEWAY_URL/webhooks/mollie`)

Mollie validates the URL by POSTing a test ping. The gateway returns
200 regardless of internal outcome (so retries don't pile up), then
verifies the signature + cross-checks with Mollie's API before
applying any state change.

### 5. Smoke test (test mode)

```bash
# 1. Bring up the gateway with MOLLIE_API_KEY=test_... in env
# 2. Sign in to the console as a platform admin
# 3. Navigate to /p/<test-project-id>/billing
# 4. Click "Upgrade to Pro"
# 5. Use Mollie's test cards: https://docs.mollie.com/overview/testing
#    - Approved card:   4242 4242 4242 4242 / any CVC / future expiry
#    - Declined card:   4111 1110 0030 0036
# 6. Confirm the webhook fires:
kubectl logs -n eurobase deploy/gateway --tail=200 | grep -i mollie
# 7. Confirm projects.plan flipped:
psql -c "SELECT plan FROM public.projects WHERE id = '<project>'"
```

### 6. Compliance / DPA registry

Mollie was added as a sub-processor in migration 000025. To confirm
it surfaces in the DPA report when the Billing feature is active:

```bash
curl -H "Authorization: Bearer <platform-token>" \
  https://api.eurobase.app/platform/projects/<id>/compliance/dpa-report \
  | jq '.sub_processors[] | select(.name=="Mollie")'
```

If you don't see Mollie listed, check `internal/compliance/registry.go`
→ `resolveActiveFeatures()` returns "billing" once at least one
subscription row exists for the project.

## Day-to-day operator tasks

### Manual reconciliation

When a customer reports a charge but no Pro features, almost always
it's a webhook delivery delay or signature mismatch. Verify manually:

```bash
# 1. Find the customer's Mollie payment ID (from Mollie dashboard
#    or from public.invoices)
psql -c "SELECT mollie_payment_id, status, created_at
         FROM public.invoices
         WHERE project_id = '<id>' ORDER BY created_at DESC LIMIT 5"

# 2. Pull authoritative state from Mollie:
curl -H "Authorization: Bearer $MOLLIE_API_KEY" \
  https://api.mollie.com/v2/payments/<tr_xxx> | jq

# 3. If status="paid" but our DB says otherwise, trigger the webhook
#    handler manually:
curl -X POST https://api.eurobase.app/webhooks/mollie \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -H "X-Mollie-Signature: <hex sig over body>" \
  -d "id=<tr_xxx>"
```

Computing the signature for a one-off:
```bash
echo -n "id=tr_xxx" | openssl dgst -sha256 -hmac "$MOLLIE_WEBHOOK_SECRET"
```

A proper CLI command (`eurobase admin billing reconcile <project>`)
is a planned follow-up — until then, the curl above is the way.

### Refunds

Issue refunds in the Mollie dashboard. Mollie sends a webhook with
`payment.status=refunded`; our handler updates the `invoices` row but
does NOT auto-downgrade the project. Decide per case:

- Partial refund → no plan change
- Full refund of the only successful payment + active subscription
  → cancel the subscription manually + downgrade (or wait for the
  next failed-renewal → grace → downgrade)

Every refund leaves an `audit_log` entry; the actor field is empty
because the refund originates from Mollie's dashboard, not from our
console.

### Plan changes: test → live

When you're ready to take real money:

1. In Mollie: switch organisation to live mode, generate `live_*` key
2. Update the `MOLLIE_API_KEY` value in the k8s Secret
3. Roll the gateway deployment (`kubectl rollout restart deploy/gateway -n eurobase`)
4. Check logs — should now see `billing: Mollie test mode` warning is
   GONE
5. Run a €0.01 smoke payment with a real card; refund immediately
6. Update Mollie's webhook URL in the dashboard if it points at staging

## Common failure modes

### "Pending payment" never clears

User clicked Upgrade but is still on Free. Likely causes:

| Symptom | Diagnose | Fix |
|---|---|---|
| No Mollie webhook in gateway logs | Mollie webhook URL wrong in dashboard | Update webhook URL, retry payment |
| Webhook arrives, signature mismatch in logs | `MOLLIE_WEBHOOK_SECRET` env doesn't match what's in Mollie dashboard | Rotate secret on both sides + roll gateway |
| Webhook applied, but `projects.plan` still free | Customer paid but the metadata.project_id was wrong (very rare — would be a code bug) | Manual SQL update + audit_log entry |

### "Pro" signup creates project as Free

This is **issue #70** which the new flow closes. If you see it,
verify the gateway has `MOLLIE_API_KEY` set — when unset, the
project-creation handler falls back to immediate creation (the
pre-#70 behaviour) regardless of `plan: 'pro'`. That's intentional
(local dev), but should never happen in production.

### Stuck `pending_projects` rows

The dunning sweeper deletes rows older than 24h. If you see a slug
"reserved" that shouldn't be, check `public.pending_projects`:

```sql
SELECT id, name, slug, mollie_payment_id, expires_at
FROM public.pending_projects
WHERE expires_at < now();
```

Manual cleanup:
```sql
DELETE FROM public.pending_projects WHERE id = '<id>';
```

## What's NOT in this rollout (issue references)

- **Custom plan tiers / usage-based metering** — #128 (function metering), #131 (per-org pricing)
- **Annual billing** — Mollie supports it; not enabled
- **Card-update flow inside our UI** — currently we redirect to
  Mollie's hosted payment page each time; their own customer portal
  is a planned add
- **Cross-page "Pro is pending" banner** — present on the billing page
  itself; not yet on every project page header
- **Reconciliation CLI command** — `eurobase admin billing reconcile`
  is documented but not yet implemented (use the curl path above)

## Audit log actions emitted by billing

| Action | When |
|---|---|
| `billing.subscription.created` | User starts checkout (writes the pending_payment row) |
| `billing.subscription.cancelled` | User clicks Cancel |
| `billing.payment.succeeded` | Webhook: payment.status=paid |
| `billing.payment.failed` | Webhook: payment.status=failed/expired/canceled |
| `billing.plan.changed` | projects.plan transitions (free→pro on first paid, pro→free on grace expiry or period end) |

Visible in the Compliance → Audit Log tab of the affected project.
