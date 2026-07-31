# Mollie webhook state machine (PR 4)

## Endpoint

`POST /platform/billing/webhook` — **unauthenticated**. Registered outside
the platform-auth middleware because Mollie is not a Eurobase user.

Body: `application/x-www-form-urlencoded` with a single `id` field.
Mollie's webhook contract is minimal by design; the ID must be
re-fetched to get canonical state.

## Trust model (Mollie does not sign)

Mollie does *not* HMAC-sign webhook POSTs. Trust rests on:

1. **URL secrecy.** The webhook URL is registered per payment /
   subscription at creation time; only Mollie and Eurobase know it.
2. **GET-back for canonical state.** The handler receives `{id}` and
   immediately calls Mollie's API for the current state. A malicious
   POST with a random `tr_...` value either hits Mollie's 404 → the
   handler returns 200 no-op, or lands on someone else's ID which
   we don't own → no local state change.
3. **Idempotency at every write.** Every DB update is either
   status-guarded (`WHERE status='active'`) or upsert-shaped
   (`ON CONFLICT DO NOTHING` on the payment ID), so a retried or
   duplicated webhook is a no-op.

Additional defence (deferred): rate-limit the endpoint to 60 req/min
per source IP once we observe real traffic patterns.

## Every branch returns HTTP 200

Mollie retries any non-2xx response. We would rather log-and-continue
than trigger a retry storm on a transient bug, so the handler
returns 200 on every terminal branch. Unrecoverable errors are
logged with the `billing.webhook.failed` slog line so a log-based
Grafana alert can fire.

## ID prefix routing

| Prefix | Resource | Handled? |
|---|---|---|
| `tr_` | Payment | Yes — `processPaymentWebhook` |
| `sub_` | Subscription | Yes — `processSubscriptionWebhook` |
| `re_` | Refund | No — created by our own action in PR 8, no webhook needed |
| anything else | Unknown future Mollie type | No — logged and ignored |

## Payment state transitions

Every payment webhook triggers a fresh GET to `/v2/payments/{id}`
for canonical state.

| `Payment.status` | `Payment.sequenceType` | Action |
|---|---|---|
| `paid` | `first` | **Activate.** Create the recurring Mollie subscription against the just-captured mandate (idempotency key: `activate:{sub_id}`), flip our subscription row to `active`, mark invoice paid, flip `projects.plan`. |
| `paid` | `recurring` | **Renew.** Insert new invoice row (`ON CONFLICT (mollie_payment_id) DO NOTHING`), bump `next_charge_at` from Mollie's `nextPaymentDate`, clear `past_due_since` if the previous charge had failed. |
| `paid` | `oneoff` | Log warn — nothing in our flow creates oneoff payments. |
| `failed` / `canceled` / `expired` | any | **Fail.** If there's a linked `subscriptionId`, flip subscription row to `past_due` and stamp `past_due_since=now()` **only** on the first transition (`WHERE status='active'` guard). Mark invoice `failed`. If no subscription (i.e. first-payment abandonment before mandate capture), only the invoice flips. |
| `open` / `pending` / `authorized` | any | Non-terminal — wait for the next webhook. No-op. |
| anything else | any | Log warn, no-op. |

### First-payment activation flow (the critical path)

```
Mollie POST /platform/billing/webhook  (id=tr_xxx)
        │
        ▼
GET /v2/payments/tr_xxx
        │
        ▼
status=paid, sequenceType=first, mandateId=mdt_yyy, metadata={subscription_id, project_id, plan_code}
        │
        ▼
Load current subscription state
        │
        ▼──► already active? → mark invoice paid (idempotent) → 200
        │
        ▼
POST /v2/customers/{cst}/subscriptions   (with mandateId, WithIdempotencyKey("activate:"+sub_id))
        │
        ▼
BEGIN
  UPDATE subscriptions SET mollie_subscription_id, status='active', started_at, next_charge_at
     WHERE id=$1 AND status='incomplete'
  UPDATE invoices SET status='paid', paid_at
     WHERE mollie_payment_id=$1
  UPDATE projects SET plan=$1
     WHERE id=$2
COMMIT
        │
        ▼
200
```

Idempotency for double-delivery:

- The `WHERE status='incomplete'` guard means a second delivery finds
  the row already `active` and no-ops.
- The `WithIdempotencyKey("activate:"+sub_id)` on `CreateSubscription`
  means Mollie returns the same subscription ID on retry.
- The `UPDATE ... SET paid_at = COALESCE(paid_at, now())` preserves
  the original `paid_at` on re-runs.

## Subscription state transitions

Fired by Mollie when subscription-level state changes (customer
cancels, fixed-term completes). Body carries `id=sub_xxx`.

| `Subscription.status` | Action |
|---|---|
| `canceled` | `subscriptions.status='canceled'`, `canceled_at=now()` (guarded by `WHERE status IN ('active','past_due')`). |
| `completed` | `subscriptions.status='expired'`, `canceled_at=now()`. Rare — our subs have no fixed term. |
| `active` / `pending` / `suspended` | No-op. Payment webhook handles state tracking; `suspended` is Mollie's soft warning that the payment webhook will report. |
| anything else | Log warn, no-op. |

## Test coverage

Deterministic pool-less unit tests cover:

- HTTP-level dispatch (missing ID → 200, disabled env → 200)
- Resource-type routing (`tr_` / `sub_` / unknown prefix)
- Foreign-ID resilience (Mollie 404 → 200 no-op)
- Server-error propagation (Mollie 500 → wrapped `ErrServer`)
- `parseMollieDate` — YYYY-MM-DD parsing, empty and garbage input

**Not covered by unit tests:** the DB state transitions themselves.
Rationale: CI's `test-go` runs plain `go test` without a Postgres
service, so DB tests would be silent no-ops. The state transitions
are covered by the manual staging QA loop (test-mode Mollie flows
through the actual DB) plus the built-in idempotency guards. A
future PR could add a testcontainers-based integration suite for
the state machine specifically.

## Configuration

No new env vars beyond PR 3. `WebhookBaseURL` (from PR 3's
`PLATFORM_BASE_URL`) is the base for the URL Mollie POSTs to.

## Manual QA (test-mode)

1. Complete a first payment via the checkout URL from PR 3.
2. Mollie's checkout page has a "select outcome" dropdown — pick
   **Paid**. Mollie fires the webhook within ~1s.
3. Verify DB:
   - `subscriptions.status='active'`, `mollie_subscription_id` set,
     `next_charge_at` populated.
   - `invoices.status='paid'`, `paid_at` set.
   - `projects.plan='pro'`.
4. Repeat with **Failed** to exercise the past_due path:
   - `subscriptions.status='past_due'`, `past_due_since` set.
   - `invoices.status='failed'`.

Cancel a subscription via Mollie's dashboard to test the
subscription webhook — `subscriptions.status` should flip to
`canceled` and `canceled_at` populated.

## Follow-ups

- **Rate-limit** the endpoint (60/min per IP). Deferred; add when we
  see real webhook traffic patterns.
- **Testcontainers state-machine tests.** Deferred; PR 5's cron
  work will need similar infrastructure — batch them together.
