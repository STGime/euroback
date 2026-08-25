# Billing checkout flow (PR 3)

## Sequence

```
Console                Gateway                DB                     Mollie
   │                     │                     │                        │
   │  POST                │                     │                        │
   │  /platform/billing/  │                     │                        │
   │  checkout            │                     │                        │
   │  {project_id,        │                     │                        │
   │   plan_code}         │                     │                        │
   ├────────────────────► │                     │                        │
   │                     │─ verify ownership ─►│                        │
   │                     │◄────────────────────│                        │
   │                     │─ check no live sub ►│                        │
   │                     │◄────────────────────│                        │
   │                     │─ read/upsert mollie_customer_id ►│           │
   │                     │◄────────────────────│                        │
   │                     │ (if new customer)   │                        │
   │                     │─ POST /customers ─────────────────────────► │
   │                     │◄──────────────────────────────────────────  │
   │                     │─ UPDATE platform_users.mollie_customer_id ►│
   │                     │                     │                        │
   │                     │─ BEGIN ─────────────►                        │
   │                     │─ INSERT subscriptions (status='incomplete') ►│
   │                     │  (23505 → ErrAlreadySubscribed)              │
   │                     │─ POST /payments ──────────────────────────►│
   │                     │  sequenceType=first, customerId, metadata={…}│
   │                     │◄──────────────────────────────── {id, _links.checkout.href}
   │                     │─ INSERT invoices (mollie_payment_id) ──►    │
   │                     │─ COMMIT ────────────►                        │
   │                     │                     │                        │
   │◄─── 200 { subscription_id, checkout_url } │                        │
   │                     │                     │                        │
   │─── window.location = checkout_url ────────────────────────────────►│
   │                                                                   Mollie
   │                                                                   handles
   │                                                                   the SEPA
   │                                                                   mandate /
   │                                                                   card 3DS
   │                                                                    │
   │◄────── redirect to /p/{id}/billing?status=success ─────────────────│
```

The Mollie subscription itself is *not* created here — that happens
in PR 4's webhook handler once the first payment is `paid` and a
mandate is on file. Our `subscriptions` row sits in `incomplete`
until then; the console shows "awaiting first payment" if it
appears.

## Idempotency

Two keys protect against duplicates:

1. **`WithIdempotencyKey("customer:" + user_id)`** on
   `CreateCustomer`. A retried checkout after a network flake won't
   create a second Mollie customer for the same user, so
   `platform_users.mollie_customer_id` stays consistent.

2. **`WithIdempotencyKey("checkout:" + subscription_id)`** on
   `CreatePayment`. Since `subscription_id` is our freshly-minted
   UUID inside the transaction, two concurrent checkouts each get
   different UUIDs — so this key protects only against a River-style
   retry of the same subscription attempt, not against the concurrent
   case. The concurrent case is handled by the `idx_subscriptions_project_live`
   UNIQUE partial index, which surfaces as `ErrAlreadySubscribed` /
   HTTP 409.

## Configuration

Read from env in `cmd/gateway/main.go`:

| Env var | Default | Purpose |
|---|---|---|
| `BILLING_ENABLED` | `false` | Master switch. `true` enables the route; anything else makes the handler return 503. |
| `MOLLIE_ENV` | `test` | `test` or `live`. Anything else is silently coerced to `test` **with a warning log** — see PR 1 review. |
| `MOLLIE_API_KEY_TEST` | *(unset)* | Bearer token for Mollie test mode. |
| `MOLLIE_API_KEY_LIVE` | *(unset)* | Bearer token for Mollie live mode. |
| `CONSOLE_BASE_URL` | `https://console.eurobase.app` | Used to build the post-checkout redirect URL. |
| `PLATFORM_BASE_URL` | `https://api.eurobase.app` | Used to build the webhook URL Mollie will POST to. |

## Curl test (against staging with `BILLING_ENABLED=true`)

```bash
# 1. Sign in as a platform user, capture the JWT.
TOKEN=$(curl -s -X POST https://api.eurobase.app/platform/auth/signin \
  -H 'Content-Type: application/json' \
  -d '{"email":"stefan@example.com","password":"…"}' \
  | jq -r .access_token)

# 2. Grab a project ID you own.
PROJECT_ID=$(curl -s https://api.eurobase.app/platform/projects \
  -H "Authorization: Bearer $TOKEN" \
  | jq -r '.projects[0].id')

# 3. Start checkout.
curl -s -X POST https://api.eurobase.app/platform/billing/checkout \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{\"project_id\":\"$PROJECT_ID\",\"plan_code\":\"pro\"}"
# → { "subscription_id": "…", "checkout_url": "https://www.mollie.com/checkout/…" }
```

Open the `checkout_url` in a browser to complete the (test-mode)
first payment. In test mode Mollie shows a "select outcome" screen
so you can drive the state machine without a real card.

## Failure modes

| Case | Status | JSON `error` |
|---|---|---|
| `BILLING_ENABLED=false` | 503 | `billing_disabled` |
| No auth token | 401 | `unauthorized` |
| Bad JSON body | 400 | `invalid_body` |
| Missing `project_id` | 400 | `missing_project_id` |
| Missing `plan_code` | 400 | `missing_plan_code` |
| Unknown plan (or `team` right now) | 400 | `invalid_plan` |
| Project not found or not owned by caller | 404 | `project_not_found` |
| Live subscription already exists (pre-check *or* unique-index race) | 409 | `already_subscribed` |
| Any other failure | 500 | `internal_error` (details in `slog`) |
