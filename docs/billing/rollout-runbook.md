# Billing rollout runbook (PR 8)

Sequence for flipping `BILLING_ENABLED=true` in prod and
launching paid Pro. Every step is scripted so it can be paused
between phases if something surprising happens.

## Pre-flight — once KYC is complete

Everything here is copy-pasteable. Order matters only for the
Mollie-secret step (the pod won't pick up the live key until
it's in the Secret before restart).

### 1. Get the live key from Mollie

Mollie dashboard → **Developers → API keys** → copy the string
starting with `live_`. Only visible after KYC completes.

### 2. Confirm the payout IBAN

Mollie dashboard → **Settings → Payouts**. Must show the
Estonian business IBAN (Eurobase OÜ). If it says "pending
verification", Mollie hasn't finished KYC yet — stop here and
resume this runbook when it clears.

### 3. Patch `eurobase-secrets` with the three billing keys

```bash
kubectl -n eurobase patch secret eurobase-secrets --type merge -p '{
  "stringData": {
    "MOLLIE_API_KEY_LIVE": "live_XXXXXXXX_paste_from_dashboard",
    "MOLLIE_ENV":          "live",
    "BILLING_ENABLED":     "true"
  }
}'
```

**Don't restart the pod yet** — restart is Step 8 below (T-0).
This step just gets the values into the Secret so the next
restart picks them up atomically.

### 4. Verify the migrations landed in prod

```sql
SELECT column_name
  FROM information_schema.columns
 WHERE table_name = 'invoices'
   AND column_name IN
       ('invoice_number', 'invoice_mail_sent_at',
        'subscription_id', 'pdf_object_key');
-- Should return 4 rows.

SELECT column_name
  FROM information_schema.columns
 WHERE table_name = 'projects'
   AND column_name = 'legacy_pro_grace_until';
-- Should return 1 row.

SELECT relname FROM pg_class WHERE relname = 'invoice_number_seq';
-- Should return 1 row.
```

If any row-count is off, the migrations haven't all applied —
resolve before proceeding.

### 5. Create the invoices bucket (idempotent)

```bash
AWS_PROFILE=scw ./deploy/scripts/create-invoices-bucket.sh
```

Script no-ops if `eurobase-platform-invoices` already exists.

### 6. Load Cockpit alert rules

Import `deploy/k8s/cockpit/alerts.yaml` into Grafana Alerting
(the file's header comment has the three import methods). The
two rules to verify exist post-import:
`BillingWebhookFailingSpike`, `BillingWebhookFailingSustained`.

### 7. Estonian accountant sign-off on the invoice PDF

Render a sample invoice against staging (any Pro sub in
test-mode produces one). Send to the accountant, confirm the
VAT §19 wording is exactly what they'd want on a real invoice
before any real one goes out.

### 8. Ping Discord `#alerts` end-to-end

Fire a deliberate broken webhook against staging:
```bash
curl -X POST https://staging-api.eurobase.app/platform/billing/webhook \
  -d 'id=tr_definitely_not_real'
```
Wait ≤10 min for `BillingWebhookFailingSpike` to fire in
Discord. If nothing lands, the alert wiring is broken — fix
before the prod flip.

## Cohort mail 24h ahead

Send to the ~1% of closed-beta users currently on Pro
(`SELECT DISTINCT u.email FROM platform_users u JOIN projects p
ON p.owner_id = u.id WHERE p.plan = 'pro'`). Subject: **Pro on
Eurobase — 14 days to add a card**.

Body template lives at
`docs/emails/2026-XX-XX-billing-goes-live.html` (create per
run; use the drip infrastructure or bulk-send helper). Include:
- One-line explanation ("Pro is now paid, €19/mo per project")
- Deep link per project: `/p/{id}/billing?plan=pro`
- 14-day grace-period countdown

Not sent to Free-tier users — they see nothing until they
choose to upgrade.

## Day-of sequence

### T-1h — flip staging one final time

Ensure `BILLING_ENABLED=true` still works end to end in staging.
Run manual QA:
1. Fresh signup → create project → upgrade to Pro → Mollie test
   payment (Paid outcome) → verify subscription active, invoice
   emailed with PDF link.
2. Cancel end-of-period → verify subscription's `canceled_at`
   stamped, `plan='pro'` retained.
3. Cancel immediate on a new sub → verify Mollie refund
   created, `plan='free'`, prorated refund arrives.
4. Legacy-Pro modal appears for the seeded `legacy_pro_grace_until`
   project.

### T-0 — flip prod

Pre-flight step 3 already put `MOLLIE_API_KEY_LIVE`,
`MOLLIE_ENV=live`, and `BILLING_ENABLED=true` into
`eurobase-secrets`. This step just rolls the pod so the
process picks them up:

```bash
kubectl -n eurobase rollout restart deploy/gateway
kubectl -n eurobase rollout status deploy/gateway --timeout=2m
```

Verify **all three** before proceeding to the smoke test:

1. **Startup log shows enabled + live mode:**
   ```bash
   kubectl -n eurobase logs -l app=gateway --tail=200 \
     | grep 'billing: enabled'
   ```
   Should print `mollie_env=live`. If it shows `mollie_env=test`
   the ConfigMap didn't merge — check step 3.

2. **No API-key prefix mismatch warning** — the client logs
   `mollie: live env but API key does not start with live_` in
   the first minute after boot if the key was pasted wrong.
   Grep for it and abort if it appears:
   ```bash
   kubectl -n eurobase logs -l app=gateway --tail=200 \
     | grep 'mollie:.*prefix' || echo "OK — no prefix warnings"
   ```

3. **Checkout call actually reaches Mollie** — from any dev
   machine with a real platform JWT for a Free-tier project:
   ```bash
   curl -sX POST https://api.eurobase.app/platform/billing/checkout \
     -H "Authorization: Bearer $TOKEN" \
     -H 'Content-Type: application/json' \
     -d '{"project_id":"...","plan_code":"pro"}' | jq
   ```
   Should return `{"subscription_id":"...","checkout_url":"https://www.mollie.com/checkout/..."}`.

If any of the three fails, roll the flag back (rollback
section) before opening the door wider.

### T+5m — smoke test

Stefan pays €0.01 on his own account (using a Mollie sandbox
card in live-mode test — see Mollie docs). Verify:
- Webhook arrives (`kubectl logs | grep billing.webhook`).
- Subscription flips to `active`.
- Invoice PDF renders + lands on `billing@eurobase.app`.
- Console `/billing` page shows the invoice + PDF download.

If everything's green, upgrade to a real €19 sub on Stefan's
personal Pro project as the final smoke test.

### T+15m — announce

Post to Discord `#launch`:

> 🎉 Eurobase Pro is now paid — €19/mo per project. Closed-beta
> Pro users: check your email for the 14-day grace window.
> Everyone else: upgrade from your project's Billing tab
> whenever you're ready.

Update marketing site `/pricing` Pro button from "Coming soon"
to "Get Pro" (redirects to console `/pricing`).

### T+24h — first-day check

```sql
-- Subscription funnel
SELECT status, count(*) FROM subscriptions GROUP BY status;
-- Failed webhooks
SELECT count(*) FROM invoices WHERE status = 'failed';
-- Legacy-Pro conversions vs remaining grace population
SELECT
  count(*) FILTER (WHERE plan='pro' AND legacy_pro_grace_until IS NULL) AS converted,
  count(*) FILTER (WHERE plan='pro' AND legacy_pro_grace_until IS NOT NULL) AS pending,
  count(*) FILTER (WHERE plan='free' AND legacy_pro_grace_until IS NULL) AS downgraded_or_never_legacy
FROM projects;
```

## Rollback

If something fatal shows up:

1. **Flip the flag off first — stops the bleeding:**
   `kubectl -n eurobase set env deploy/gateway BILLING_ENABLED=false`
   then `kubectl rollout restart deploy/gateway`. Endpoints
   revert to 503 `billing_disabled` within seconds.

2. **Cancel existing Mollie subscriptions.** They'll keep
   charging users until explicitly stopped — our webhooks are
   now silently 200'ing (see PR 4), so we won't record the
   charges but Mollie will still make them. Two options
   depending on volume:

   **≤5 subscriptions** — manual cancel from the Mollie
   dashboard (`my.mollie.com` → Customers → each customer →
   Subscriptions → Cancel).

   **>5 subscriptions** — run the mass-cancel script:

   ```bash
   MOLLIE_API_KEY=live_xxx \
   DATABASE_URL=postgres://... \
     ./deploy/scripts/mass-cancel-mollie-subscriptions.sh
   ```

   Script is idempotent (already-canceled subs return 404 →
   treated as success) and rate-limited to 1 req/s so it's
   safe against Mollie's rate limits. Prompts before doing
   anything destructive.

3. **Refund any charges users didn't sign off on** — Mollie
   dashboard → Payments → Refund. There's no bulk-refund
   equivalent; do these by hand. At >20 refunds, script the
   Refunds API by adapting the mass-cancel script.

4. **Local state stays** — `subscriptions` / `invoices` rows
   are safe to keep. Nothing depends on them until
   `BILLING_ENABLED=true`. The migrations DO NOT need to be
   rolled back — extra columns are all nullable / defaulted.

**Time-to-rollback:** ≤5 minutes on the flag flip, then ~1 min
per subscription to cancel (linear in subscription count via
the script). Aim to catch problems within the T+15m or T+24h
checkpoints below to keep the cancel population small.

## Follow-ups (post-launch)

- Grafana dashboard: MRR, active-sub count, past-due count,
  refunded-amount, failed-webhook rate.
- Auto-retry-refund worker for the "Mollie refund failed but
  local cancel proceeded" case — currently manual dashboard
  action.
- Marketing site: add "Choose Pro" button on the sales-page
  pricing table (currently only console-facing).
- Playwright coverage on the console billing pages (deferred
  from PR 7).
