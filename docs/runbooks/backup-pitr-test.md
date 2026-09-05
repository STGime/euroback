# Runbook: Team-tier backup + point-in-time-recovery test plan

## Why this runbook exists

The Team-tier pricing card advertises:

> Dedicated Postgres per project · **7-day scheduled backups + 7-day point-in-time recovery + 1 restore/month included** · SSO · RBAC · audit trail · SOC 2.

Before the Team tier opens to paying customers, that promise has to be
proven end-to-end against a real Scaleway RDB instance. This runbook
is the test plan and the pass/fail gate for the launch.

## Hard constraints

The Team-tier promise, translated into measurable claims:

| # | Claim | Measurable |
|---|---|---|
| C1 | Scheduled backups exist and run daily | `scw rdb backup list` returns ≥1 backup per calendar day, going back 7 days |
| C2 | 7-day retention on scheduled backups | The oldest backup is < 8 days old; nothing older |
| C3 | Point-in-time recovery is available for any moment in the last 7 days | Restore to a chosen timestamp lands the DB in exactly that state |
| C4 | RPO (max data loss on unplanned failure) is ≤ 5 minutes | Continuous WAL archiving — Scaleway RDB claim, verify by inspecting oldest restorable timestamp gap |
| C5 | RTO (time to restore) meets the customer expectation | Full restore of a 1 GB DB completes in < 15 min (Scaleway's stated target; verify) |
| C6 | Restore does not touch other projects | Restore of project A leaves project B untouched (dedicated-Postgres-per-project guarantee) |
| C7 | 1 restore/month included, further restores metered/paywalled | Restore attempt N+1 in a calendar month either fails cleanly or triggers billing — enforcement lives in our platform, not Scaleway |
| C8 | Backup ciphertext stays in the EU | Scaleway RDB snapshots in fr-par region only; audit-log the S3 bucket location |

C1–C6 are Scaleway-plumbing tests. C7 is our platform-code test.

## Prerequisites

- One Team-tier-shaped dedicated RDB instance (`DB-DEV-S`, PostgreSQL 16,
  1 GB storage, `--backup-schedule-frequency=24` `--backup-schedule-retention=7`).
  Provisioned via `scw rdb instance create` in fr-par with the same
  parameters the provision-tenant worker will use for a real Team-tier
  project. Do NOT re-use an existing prod or shared instance.
- `scw` CLI 2.62.x+ authenticated with the SCW keys used by the platform
  provisioner (same role/permissions as production).
- `psql` client and `pg_dump` / `pg_restore` locally.
- A test project on Team tier in staging (or a placeholder that lets us
  wire the console restore UI once it exists — see the "Follow-ups"
  section at the bottom).
- **Verify the `scw` subcommand surface before running T3 / T4** —
  Scaleway's RDB CLI has shifted across versions. Confirm against the
  installed 2.62.x binary:
  ```sh
  scw rdb backup list --help          # T1
  scw rdb backup create --help        # T2
  scw rdb backup restore --help       # T2
  scw rdb instance clone --help       # T3 (must accept point-in-time=)
  scw rdb instance get --help         # T4 (JSON must include
                                      #     restore_from_time / restore_to_time)
  ```
  If a flag or JSON field has been renamed, patch the runbook commands
  before executing; do not "wing it".

## Test data model

Small enough to restore quickly, structured enough to exercise
timestamps, FK relations, and typical BaaS shapes:

```sql
-- Users (auth-shaped)
CREATE TABLE users (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  email TEXT UNIQUE NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Documents (application-shaped, FK to users)
CREATE TABLE documents (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  owner_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  title TEXT NOT NULL,
  body TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Audit-log-shaped table (append-only, order-sensitive)
CREATE TABLE events (
  id BIGSERIAL PRIMARY KEY,
  actor_email TEXT NOT NULL,
  action TEXT NOT NULL,
  at TIMESTAMPTZ NOT NULL DEFAULT now(),
  payload JSONB
);

-- Manifest table — stamps every test run with a checksum row so
-- verify-restore.sh can prove which state it landed in.
CREATE TABLE test_manifest (
  checkpoint_name TEXT PRIMARY KEY,
  taken_at TIMESTAMPTZ NOT NULL,
  row_count_users BIGINT NOT NULL,
  row_count_documents BIGINT NOT NULL,
  row_count_events BIGINT NOT NULL,
  checksum_documents TEXT NOT NULL   -- md5(string_agg(id::text || body, ',' ORDER BY id))
);
```

`scripts/ops/seed-backup-pitr-test-data.sh` in the euroback repo populates these
and stamps `test_manifest` after each mutation phase.
`scripts/ops/verify-restore.sh` compares the current DB state against
a named manifest row.

## Test scenarios

Each scenario has: **setup → action → expected result → pass/fail**.
Run them in order; each one's setup depends on the previous scenario's
final state.

### T1 — Baseline: automatic scheduled backup exists

**Setup:**
1. Fresh RDB instance, seeded via `scripts/ops/seed-backup-pitr-test-data.sh` with
   1,000 users, 5,000 documents, 20,000 events. Stamp manifest row
   `checkpoint_name='baseline'`.
2. Wait > 24 h so at least one scheduled backup has run.

**Action:**
```sh
scw rdb backup list instance-id=<id> region=fr-par -o json | jq
```

**Expected:**
- ≥ 1 backup with `status=ready`, `created_at` within last 24 h.
- `automated=true` (not manually triggered).

**Fail case:** no scheduled backup after > 24 h → open ticket with
Scaleway; blocks Team tier launch until resolved.

### T2 — Manual backup + restore to a NEW instance

Manual snapshot is what the customer-facing "trigger a restore" flow
runs. Verifies the CLI/API surface, restore mechanics, and data
integrity.

**Setup:** state from T1.

**Action:**
```sh
# 1. Take a manual backup, note its ID.
scw rdb backup create instance-id=<id> name=t2-manual region=fr-par
BACKUP_ID=<from output>

# 2. Wait for backup status=ready.
scw rdb backup wait <BACKUP_ID> region=fr-par

# 3. Restore into a fresh instance (destination-inclusive; source
#    instance is untouched).
scw rdb backup restore <BACKUP_ID> \
  instance-id=<new-target-instance-id> region=fr-par
```

**Expected:**
- Restore completes within 15 min for the 1 GB dataset (measure).
- `scripts/ops/verify-restore.sh --manifest=baseline` against the
  restored instance returns 0 (all row counts + checksums match).
- Source instance is completely untouched (verify via `verify-restore.sh`
  on the source, still passes `baseline`).

**Fail case:** row count or checksum mismatch → data integrity bug in
Scaleway RDB, blocking launch. Any restore > 30 min → RTO promise
needs rewording before launch.

### T3 — PITR: restore to a specific timestamp

The core Team-tier claim. Verify that we can land the DB in exactly
the state it was at any chosen moment in the last 7 days.

**Setup:** state from T2 (source instance still has the baseline data).

**Action:**
```sh
# 1. Record timestamp T_A. Seed a small mutation batch.
T_A=$(date -u +%Y-%m-%dT%H:%M:%SZ)
psql "$SOURCE_URL" -c "
  INSERT INTO events (actor_email, action) VALUES
    ('alice@test', 'batch-A-event-1'),
    ('alice@test', 'batch-A-event-2');
  INSERT INTO test_manifest ...  -- checkpoint 'batch-A'
"

sleep 30  # give WAL archiving time to catch up

# 2. Record T_B. Second mutation batch.
T_B=$(date -u +%Y-%m-%dT%H:%M:%SZ)
psql "$SOURCE_URL" -c "
  INSERT INTO events (actor_email, action) VALUES
    ('bob@test', 'batch-B-event-1');
  DELETE FROM documents WHERE title LIKE 'to-delete-%';
  INSERT INTO test_manifest ...  -- checkpoint 'batch-B'
"

sleep 30

# 3. Record T_C. Third batch.
T_C=$(date -u +%Y-%m-%dT%H:%M:%SZ)
psql "$SOURCE_URL" -c "..."  # checkpoint 'batch-C'

# 4. PITR restore to T_B (between batches A and B, before C).
scw rdb instance clone <source-id> \
  point-in-time="$T_B" \
  region=fr-par
CLONE_ID=<from output>
```

**Expected on the CLONE:**
- `batch-A` events present (both alice@test rows).
- `batch-B` events present (bob@test row).
- `batch-C` events **absent**.
- `documents` where `title LIKE 'to-delete-%'` **absent** (deleted at T_B).
- `test_manifest` has rows for `baseline`, `batch-A`, `batch-B` but not
  `batch-C`.

Verified via `verify-restore.sh --manifest=batch-B`.

**Fail case:**
- If C rows appear on the clone → PITR is not respecting the
  timestamp; blocker.
- If B rows are missing → WAL archiving lag > 30s (bad); either
  investigate + fix or advertise RPO honestly (e.g. "≤ 2 min").

### T4 — RPO measurement: how much data can we lose?

**Setup:** state from T3 clone; abandon the clone, work on source
instance.

**Action:**
```sh
# Insert one row per second for 60 s, each stamped with its wall-clock
# timestamp.
for i in $(seq 1 60); do
  psql "$SOURCE_URL" -c "INSERT INTO events (actor_email, action, payload)
    VALUES ('rpo-test', 'tick', jsonb_build_object('t', now()));"
  sleep 1
done

# Query the oldest restorable timestamp from Scaleway API.
scw rdb instance get <source-id> region=fr-par -o json \
  | jq '{restorable_from: .restore_from_time, restorable_until: .restore_to_time}'

# Now clone to the *most recent* restorable timestamp minus 5 s and
# compare "highest 'tick' row" between source and clone.
```

**Expected:**
- Gap between the source's latest row and the clone's latest row is
  ≤ 5 seconds.
- This measures actual RPO (not the marketing claim).

**Fail case:** gap > 5 min → the "≤ 5 minute" implicit promise needs
either a real explicit-in-writing SLA number or a claim update.

### T5 — RTO measurement: how long does a restore take?

**Setup:** an instance seeded with a scale-representative dataset (see
below).

**Action:**
- Time three full-restore cycles at three data sizes:
  - **Small:** 100 MB (typical Free/Pro moving to Team on day 1).
  - **Medium:** 1 GB (mid-sized Team-tier customer).
  - **Large:** 10 GB (upper bound of what a single Team-tier project
    would carry before splitting).

For each: capture wall-clock time from `scw rdb backup restore` return
to `instance status=ready`.

**Expected:** median < 15 min for small, < 30 min for medium, < 90 min
for large. Numbers become the honest customer-facing RTO in the DPA /
status page.

**Fail case:** > 30 min for small → block launch, investigate with
Scaleway.

### T6 — Cross-project isolation

**Setup:** provision a second dedicated RDB instance (`instance-B`),
seed with a distinct dataset (checkpoint `project-B-baseline`).

**Action:**
- Trigger a PITR clone of `instance-A` (from T3).
- While the clone runs, and after it completes, run
  `verify-restore.sh --manifest=project-B-baseline` against
  `instance-B`.

**Expected:** `instance-B` is unaffected, all row counts and checksums
match the `project-B-baseline` manifest exactly.

**Fail case:** *any* drift on instance-B → dedicated-per-project
promise is broken, launch blocked.

### T7 — Restore quota enforcement (platform code, not Scaleway)

Team tier: **1 restore/month included, further restores metered or
paywalled**. Scaleway does not enforce this — it's our billing/platform
code. So the test is against our code, not Scaleway.

**Setup:**
- A test project on Team tier in staging billing environment
  (`BILLING_ENABLED=true`, `MOLLIE_ENV=test`).
- Zero restores used this billing period.

**Action:**
1. Trigger restore #1 via the customer-facing endpoint (`POST
   /platform/projects/{id}/restore` — build this endpoint if it does
   not exist yet; see follow-up).
2. Verify: succeeds, `project_restore_events` table shows one row,
   `restores_used_this_period=1`.
3. Trigger restore #2 immediately.
4. Verify: either (a) returns 402 with a clear message about the
   quota + upgrade path, or (b) succeeds and inserts a Mollie invoice
   line item for the overage — pick one, document, ship consistently.

**Expected:** either explicit soft-fail with clear billing signal OR
successful metered overage — never a silent success that costs the
customer money without an invoice line.

**Fail case:** restore #2 silently succeeds with no billing record →
customer surprise on next invoice; blocking.

### T8 — Backup ciphertext location + encryption

**Setup:** T2's manual backup exists.

**Action:**
```sh
scw rdb backup get <BACKUP_ID> region=fr-par -o json \
  | jq '{region, download_url, encryption_at_rest}'
```

**Expected:**
- `region: fr-par`.
- `encryption_at_rest: true` (Scaleway RDB default, verify).
- If `download_url` is present, its hostname resolves to a Scaleway
  Object Storage bucket in fr-par (not a shared / global CDN).

**Fail case:** any non-EU location in the backup path → sovereignty
regression; blocking.

## Success criteria — the pass/fail gate

Team-tier launch is gated on **all 8 tests passing**. If any test
fails:
- **T1, T2, T6, T8** are blockers on Scaleway plumbing → open a
  ticket, do not launch.
- **T3, T4, T5** measurement failures may allow launch with revised
  claim wording (e.g. "10-minute RPO" instead of "5-minute") — legal
  + product must approve the revision before customer comms go out.
- **T7** is a platform-code bug → fix in a follow-up PR, do not
  launch until green.

## Rollback plan (if a real customer restore goes wrong)

Every restore creates a NEW instance (Scaleway pattern for both
manual backup restore and PITR clone). The source instance is
untouched. So the rollback is:
1. Point the app back at the source instance's `DATABASE_URL`.
2. Delete the failed clone.
3. Investigate offline.

Customer impact: zero, because the source was never modified. This
is a nice property of Scaleway's restore model — worth surfacing in
the customer-facing restore UI.

## Follow-ups (out of scope for this runbook but required before GA)

- **Console restore UI** — no UI today. Need `POST /platform/projects/{id}/restore` endpoint + a "Restore" tab on the Team-tier project settings that lets the customer pick a timestamp (calendar picker for the last 7 days). File as a separate issue.
- **`project_restore_events` table** — new schema needed. Records who triggered the restore, when, target timestamp, source backup id, clone instance id, quota-period ordinal (1st / 2nd / ...). Migration in the same PR as the endpoint.
- **Mollie overage line-item** — if T7 chooses the metered path over the paywalled path. `internal/billing/overages.go` doesn't exist today.
- **Status page: RTO/RPO published numbers** — derived from T4 + T5 measurements. Copy into `/security` page + DPA.
- **Automated recurring test** — this runbook is a one-shot pre-launch gate. Post-launch, a monthly cron that runs T3 + T4 against a synthetic project would catch Scaleway regressions before customers do. Separate issue.

## Refs

- Team-tier pricing card: `eurobase/src/data/content.ts` (hero.tiers Team entry)
- Scaleway RDB docs: https://www.scaleway.com/en/docs/managed-databases-for-postgresql-and-mysql/
- `CLAUDE.md` → Team-tier runtime password + dedicated-DB provisioning (M2.5)
- Related planned issues (to be filed): #503 (Scaleway origin/email migration), #485 (PgBouncer + read replicas), #302 (BYO S3 bucket)
