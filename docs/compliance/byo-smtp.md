# BYO Custom SMTP

> #235 Part 1. Per-project escape hatch from the platform email path.
> Lets a project send auth + transactional email through its own SMTP
> provider, with the credential sealed at rest using the per-tenant
> HKDF key.

## Why

The platform email path goes through the single shared Scaleway TEM
sender. That works for low-volume projects, but it means:

- The platform `EmailsPerHour` ceiling is a hard limit (a project
  that outgrows it can only ask us to raise a number).
- Every project shares the platform's sender reputation and
  deliverability.
- Customers can't customise the From address or domain past the
  template-level overrides.

BYO custom SMTP solves all three: the project plugs in its own
provider, the platform stops being the limit, and the project owns
its sender reputation. Future Part 2 (#235, blocked on Mollie
billing) will add a paid Scaleway-TEM-hosted add-on for projects that
want EU-sovereign managed sending without standing up their own SMTP.

## Surface

```
GET    /platform/projects/{id}/email-sender         — load config (no password)
PUT    /platform/projects/{id}/email-sender         — upsert config + seal password
DELETE /platform/projects/{id}/email-sender         — clear, fall back to platform
POST   /platform/projects/{id}/email-sender/test    — send verification email, mark verified
```

All routes are admin-only. Console UI: project Auth page → **SMTP** tab.

## Flow

1. Admin enters host / port / username / password / from / encryption.
2. PUT seals the password (AES-256-GCM with the per-tenant HKDF key —
   same mechanism as edge_functions env_vars #206) and persists the
   row. `verified_at` is cleared on every config change.
3. Admin runs **Send test** to a chosen address. The backend dials the
   provider, authenticates, sends a small "your custom SMTP works"
   message. On success: `verified_at = now()`. On failure:
   `last_error` + `last_error_at` recorded.
4. Once verified, auth emails (verification, password reset, magic
   link) start routing through the custom SMTP instead of the platform
   TEM client.
5. If the test fails after a previous success, the auth path keeps
   using the *last verified* config — `verified_at` is reset only
   by a config change (Upsert) or by Disconnect (Delete). A test
   failure records `last_error` + `last_error_at` for the operator
   to see, but does NOT silently flip the project back to the
   platform sender. A transient provider blip during a manual test
   would otherwise cause an invisible reputation switch on the
   next signup, which is exactly what the verify-first flow is
   supposed to prevent (see review #1 on PR #245).

## What routes through the custom SMTP

| Send                                | Custom SMTP when configured? |
| ----------------------------------- | ---------------------------- |
| `SendVerificationEmail` (signup)    | Yes                          |
| `SendPasswordResetEmail`            | Yes                          |
| `SendMagicLinkEmail`                | Yes                          |
| `SendPlatformPasswordResetEmail` (console user) | No — platform always |
| `SendBulkBCC` (superadmin broadcasts) | No — platform always       |
| `SendRaw` (internal usage alerts)   | No — platform always         |

The rule: anything emitted on behalf of a tenant goes through that
tenant's sender; anything platform-emitted (console password resets,
infra alerts, beta-allowlist invites) stays on the platform path.

## Encryption modes

`encryption` is one of:

- `starttls` — connect plaintext on the SMTP port (587 standard),
  upgrade with `STARTTLS`. **Recommended** — almost every modern
  provider supports it and the upgrade enforces TLS ≥ 1.2 on our side.
- `tls` — direct TLS / SMTPS (port 465 standard). Same security
  posture as STARTTLS once connected.
- `none` — plaintext, no upgrade. Surfaced in the UI with a "don't
  use this over the internet" advisory; useful only for an internal
  relay on a private network.

> **Important:** Go's stdlib `smtp.PlainAuth` refuses to send
> credentials over a plaintext connection (except to `localhost`).
> So `encryption=none` + a non-empty username/password will fail at
> auth time with a clear error from stdlib (`unencrypted connection`),
> not silently leak the password on the wire. Use `none` only for an
> internal relay that needs no auth (rare).

## Sealed-at-rest contract

Password storage mirrors the edge_functions `env_vars` pattern from
#206:

- `password_blob`, `password_nonce`, `password_key_version` —
  AES-256-GCM ciphertext + IV + the key version used at seal time.
- All three columns are NULL together or populated together
  (CHECK constraint `password_all_or_nothing`). A "no auth needed"
  relay is represented by all-NULL.
- Sealing requires `VAULT_ENCRYPTION_KEY` on the gateway. Without it
  PUT returns 503 `vault not configured — cannot seal SMTP password`,
  surfaced in the console as a setup blocker so the operator fixes
  the secret before continuing.

The console NEVER receives the password. The API's `has_password`
boolean tells the UI whether sealed bytes exist; the UI's "leave
blank to keep saved" hint lets the operator edit non-secret fields
without re-typing the password.

## Sovereignty advisory

When the configured `host` matches a known US-based provider
(SendGrid, Mailgun, Postmark, Amazon SES, SparkPost), the backend
returns a `sovereignty_warning` string on GET, surfaced in the
console as an advisory:

> ⚠ Host matches SendGrid (US) — a US provider. Data sent through
> this SMTP leaves the EU jurisdiction; consider an EU-based provider
> (Scaleway TEM, Brevo, Mailjet, Mailtrap EU) to preserve sovereignty.

**Advisory, not block.** A tenant legitimately may choose a US
provider — the email-content is their data, they own the sovereignty
trade-off. The CLAUDE.md "no US cloud services" rule is platform-side;
tenant-owned config is the tenant's call. The advisory exists so the
operator who pastes `smtp.sendgrid.net` and hits Save sees the
consequence at the same time as their choice.

The list is intentionally short — the goal is to catch the obvious
case, not to be a comprehensive provider classifier.

## Quota interaction

The per-project `EmailsPerHour` knob is **not** enforced today (per
the #234 review the platform-side cap was parked behind BYO-SMTP
shipping). When it does wire in, the dispatcher (`sendProjectScoped`)
naturally bypasses it for project-sender sends — the project owns
the provider's own limits. The bypass shape is already correct.

## Operator runbook (post-deploy)

1. Apply migration 000071 (`make migrate` or the migrate Job).
2. Confirm `VAULT_ENCRYPTION_KEY` is set on the gateway pods (the
   PUT route refuses to seal without it).
3. In the console: Auth → SMTP tab. Fill in provider details. Save.
4. **Run a test send** — until the test succeeds, the project keeps
   using the platform sender. This is intentional: catches a
   misconfigured SMTP at setup, not silently at first signup.
5. If the test fails, the `last_error` shown in the UI is the exact
   error message the provider returned (auth failed, TLS error,
   DNS failure, etc.). Fix and re-test.

## Out of scope (Part 2)

- Paid Scaleway-TEM-hosted email add-on (managed sending domain
  + DKIM/SPF/DMARC tooling + metered billing). Blocked on the
  Mollie billing series (#157–#162). Tracked separately under #235.
- Sub-processor registry entry for the customer's SMTP provider
  (the customer chose them, not us — they're not Eurobase
  sub-processors).
- Per-project email metering / analytics dashboard.
