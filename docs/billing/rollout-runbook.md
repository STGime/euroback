# Billing rollout runbook (PR 8)

Sequence for flipping `BILLING_ENABLED=true` in prod and
launching paid Pro. Every step is scripted so it can be paused
between phases if something surprising happens.

## Pre-flight (run 24h before)

- [ ] **Mollie live-mode key** provisioned via the Mollie
  dashboard as Eurobase OÜ. Bearer token in
  `eurobase-secrets` as `MOLLIE_API_KEY_LIVE`.
- [ ] **Bank account verified for payouts** — Mollie pays to
  the Estonian business IBAN. Confirm in Mollie dashboard →
  Settings → Payouts.
- [ ] **Estonian accountant sign-off** on the invoice
  template. Verify the VAT §19 wording is exactly what they
  want to see.
- [ ] **Object Storage bucket exists** in prod:
  `AWS_PROFILE=scw ./deploy/scripts/create-invoices-bucket.sh`.
- [ ] **Migrations 000079–000082 applied** in prod (verify
  via `SELECT column_name FROM information_schema.columns
  WHERE table_name='invoices' AND column_name IN
  ('invoice_number','invoice_mail_sent_at','subscription_id',
  'pdf_object_key')` — should return 4 rows).
- [ ] **Cockpit alerts registered:**
  `BillingWebhookFailingSpike` + `BillingWebhookFailingSustained`
  from `deploy/k8s/cockpit/alerts.yaml`.
- [ ] **Discord `#alerts` receiving webhook failure test
  ping** (fire a broken webhook via `curl` against staging,
  verify the alert lands).

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

```bash
# Prerequisite: pod-config secret is already patched with
# BILLING_ENABLED=true (via SealedSecret or kubectl patch).
kubectl -n eurobase rollout restart deploy/gateway
```

Verify:
- `kubectl logs -l app=gateway | grep 'billing: enabled'`
  fires with `mollie_env=live`.
- `curl -X POST https://api.eurobase.app/platform/billing/checkout`
  with a real bearer token succeeds and returns a
  `https://www.mollie.com/checkout/...` URL.

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
