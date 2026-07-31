# Downgrade sweep (PR 5)

Hourly goroutine started in `cmd/gateway/main.go` when
`BILLING_ENABLED=true`. Two branches per tick, both idempotent:

## Branch A — past-due grace elapsed

Fires when a subscription has been in `past_due` for longer than
`pastDueGracePeriod` (**7 days**). By the time we see this:

1. Mollie has already run its own retry schedule (~3 attempts over
   ~21 days).
2. Our webhook flipped `subscriptions.status='past_due'` on the
   first failure and stamped `past_due_since=now()`.
3. Now, +7 days later, we're giving up.

Result: `subscriptions.status='expired'`, `projects.plan='free'`,
Mollie subscription canceled, notification mail sent.

## Branch B — legacy-Pro grace elapsed

Fires when a project has `plan='pro'` **and**
`legacy_pro_grace_until < now()` **and** no live subscription
(`status IN ('incomplete','active','past_due')`).

This is the migration-000080 cohort: existing beta-Pro users who
didn't add a payment method during the 14-day window after
`BILLING_ENABLED=true` shipped.

The "no live subscription" guard closes the plan-doc race —
a user who starts checkout at grace-day 13 hour 23 and completes
at grace-day 14 hour 1 has an `incomplete` (then `active`) row
that excludes them from this query.

Result: `projects.plan='free'`, `legacy_pro_grace_until=NULL`,
`last_active_at=now()` (reset so the 30-day idle-pause clock
starts fresh — kinder than "you're on Free and pause tomorrow"),
notification mail sent. No Mollie call because there's no
subscription to cancel.

## Downgrade action (shared)

1. **Cancel the Mollie subscription** (best-effort — an
   `ErrNotFound` is treated as "already gone"; any other error
   logs but does not block the local flip). Only fires on Branch
   A; Branch B has no `mollie_subscription_id`.
2. **Flip local state** in a short transaction:
   - `subscriptions.status='expired'`, `canceled_at=now()`
     (guarded by `status IN ('active','past_due','incomplete')`
     to skip already-terminal rows).
   - `projects.plan='free'`, `legacy_pro_grace_until=NULL`,
     `last_active_at=now()` (guarded by `plan='pro'` to be safe
     against concurrent state flips).
3. **Send notification mail** (best-effort — a TEM failure logs
   but does not roll back).

## Mail content

Subject: `Eurobase — <project> is on the Free plan`.

Body explains what happened (reason branch), what stays intact
(DB, storage, functions, users), what changes (Free-tier caps +
30-day idle pause), and how to re-enable Pro. Signed "Stefan".
Estonian entity footer per legalStrings.

## Cadence

Hourly. Matches the existing `audit_retention` + DSAR-cleanup
goroutines — chosen for consistency, not for the specific
downgrade timing. The user-visible precision is "within an hour
of the grace deadline"; the deadline itself is measured in days,
so the hour-scale jitter is invisible.

`StartLoop` fires once on startup so a fresh deploy processes any
backlog immediately rather than waiting up to an hour.

## Configuration

No new env vars. Reuses:

- `BILLING_ENABLED=true` — required for the sweep to start at all
- `emailService` — required for notification mail (nil-safe: no
  mail sent, downgrade still commits)
- `mollieClient` — required for Mollie sub cancellation (safe
  even with empty API key: returns ErrUnauthorized, treated as
  "can't cancel, log and proceed with local flip")

## Test coverage

Deterministic pool-less unit tests:
- Grace constants pinned (7d past-due, 1h sweep interval).
- Mail content per reason branch (past-due vs legacy-pro).
- Nil mailer no-ops without crashing.
- Empty owner email skips send.
- Mailer error doesn't propagate.

Not covered by unit tests (same rationale as PR 4): DB queries
+ Mollie cancel. CI runs `go test` without a Postgres service, so
DB tests would be silent no-ops. Exercised via manual staging QA:

1. Insert a `subscriptions` row with `status='past_due'` and
   `past_due_since = now() - interval '8 days'` for a real project.
2. Wait for the next hourly tick (or restart the pod — startup
   fire triggers it immediately).
3. Verify: `subscriptions.status='expired'`, `projects.plan='free'`,
   `last_active_at ≈ now()`, notification mail arrives.
4. Repeat for legacy-Pro branch: set `plan='pro'` +
   `legacy_pro_grace_until = now() - interval '1 hour'` on a
   project with no subscriptions.

## Rollback

Down migration `000080_legacy_pro_grace.down.sql` drops the
column + index. The `DowngradeService` no-ops harmlessly against
missing columns (queries would fail; the service catches and
logs, next tick reconciles once the schema is right).

Delete the goroutine start call in `main.go` to disable the sweep
entirely.
