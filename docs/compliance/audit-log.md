# Audit Log — Tamper-Evident Integrity

> GDPR Art. 5(2) (accountability), Art. 30 (records), Art. 32 (integrity)
> (Tier-1 #3). How `public.audit_log` is made tamper-evident and append-only,
> and how to verify it.

## What is recorded

`public.audit_log` records sensitive platform operations — auth-config
changes, API-key regeneration, vault secret set/update/delete/**rekey**,
member changes, DSAR exports, project create/delete, and MCP SQL execution.
Each row has `actor_id` / `actor_email`, `action`, optional `target`,
`metadata`, `ip_address`, and `created_at`. Action constants live in
`internal/audit/service.go`.

> Logging of end-user **data access** (who read which personal-data rows) is a
> separate, higher-volume stream — see the data-access log work (#169).

## Integrity model (migration 000058)

The log is both **tamper-evident** (you can prove if it was altered) and
**tamper-resistant** (the runtime roles cannot alter it).

### Hash chain
Every row stores:

```
row_hash = SHA-256( prev_hash || canonical(row) )
```

- `prev_hash` is the `row_hash` of the previous row in the **same project's**
  chain (`NULL` for the first row; rows with no project share one chain).
- `canonical(row)` is a fixed, separator-delimited encoding of the row's
  fields with `created_at` rendered at **UTC**, computed by the IMMUTABLE
  function `public.audit_row_hash(...)`. Both the insert path and the verifier
  call that one function, so the bytes are identical on both sides.
- A dedicated monotonic `seq` orders the chain, so integrity holds even if two
  rows share a `created_at` microsecond.

Any alteration, deletion, reordering, or mid-chain insertion changes a hash
and breaks the chain.

### Concurrency
Appends are serialized per project with a transaction advisory lock
(`pg_advisory_xact_lock`) so the read-chain-head-then-insert step is atomic
and never forks under concurrent writes (`internal/audit/service.go` `Log`).
Audit writes are low-frequency, so the lock is not a hot path — but note that
audited write latency is now **lock-bound per project**: a burst of audited
operations on the same project serializes on that lock.

### Append-only enforcement
Migration 000058 runs `REVOKE UPDATE, DELETE ON public.audit_log` from
`eurobase_gateway`. The audit service connects as **gateway** (the gateway
pool), so this is the enforcement that matters: the runtime can only `INSERT`
and `SELECT`.

The migration also revokes from `eurobase_developer`, but that is
**defence-in-depth, not the main guard**: platform/developer transactions run
`SET LOCAL ROLE eurobase_migrator` (see `CLAUDE.md`), so developer-path code
executes with migrator privileges and never touches `audit_log` as
`eurobase_developer`. `eurobase_migrator` (deploy-only, table owner) keeps
full rights by necessity.

### What the in-DB chain does and does NOT cover
The chain catches **modification, deletion, and reordering** of existing rows.
It does **not** catch, on its own:

- A **migrator/owner-level** actor rewriting the entire forward chain.
- A **compromised gateway forging new appends** — gateway retains `INSERT` and
  could write rows with attacker-chosen `prev_hash`/`row_hash`/`seq`.

The independent defence for both is the off-box **WORM dump** (signed,
append-only object-store export) shipping in the SIEM-export / retention work
(#170/#171): once hashes are exported off-box, in-DB rewriting or forgery is
detectable against the external copy.

## Verifying

```
GET /platform/projects/{id}/compliance/audit-log/verify     (admin)
```

Returns:

```json
{ "ok": true, "checked": 128 }
```

or, on a broken chain:

```json
{ "ok": false, "checked": 42, "broken_at_id": "…", "reason": "row hash mismatch — a field was altered" }
```

`Verify` (`internal/audit/verifier.go`) walks the chain in `seq` order and,
per row, checks (1) the stored `row_hash` matches a fresh recompute of the
row's fields, and (2) the row's `prev_hash` matches the previous row's
`row_hash`. The first failure is reported with the offending row id. An empty
chain verifies as OK.

> `Verify` walks the **entire** project chain in one pass (no pagination). On
> a multi-year log this is a heavy interactive call; a bounded/streaming or
> checkpointed variant is a likely future addition.

### Verify after retention pruning

When retention pruning is enabled (see below), the oldest rows of a project's
chain are deleted. Verify reads
`public.audit_log_chain_checkpoints.last_row_hash` for the project and seeds
its initial `prev` from that value, so the first **surviving** row's
`prev_hash` still links correctly. Operationally this means: in-DB Verify
keeps working without false positives across pruning events, and the full
pre-prune history is still verifiable against the off-box WORM dump (#170).

## Testing

`internal/audit/verifier_test.go` (integration, runs in CI `test-go`):
appends entries, verifies clean, then mutates and deletes rows directly in the
DB and asserts `Verify` flags both — the content-tamper and the
linkage-break paths.

## Retention (#171)

Two streams, two strategies — picked to match each stream's volume and the
GDPR Art. 30 baseline ("≥1 year").

| Task                          | Default     | Mechanism                                        |
| ----------------------------- | ----------- | ------------------------------------------------ |
| `audit_log` row prune         | never       | Per-project row prune, leaves chain checkpoint   |
| `data_access_log` partitions  | drop > 13 mo| Drop whole monthly partitions past the horizon   |
| Forward partition pre-create  | 12 mo ahead | Rolling pre-create so writes never hit `DEFAULT` |

The retention worker (`internal/workers/audit_retention.go`) runs **once at
startup + every 24 h**. Each tick calls three migrator-owned `SECURITY
DEFINER` helpers added in migration 000070:

- `public.ensure_future_data_access_log_partitions(months_ahead)` —
  idempotent rolling pre-create.
- `public.drop_old_data_access_log_partitions(cutoff_months)` —
  detaches + drops monthly partitions whose covered month ends on or before
  `today - cutoff_months`. Returns the list of dropped names.
- `public.prune_audit_log(cutoff_days)` — per project, deletes rows older
  than the cutoff, **never the chain head** (always preserves the
  highest-`seq` row), and upserts the chain checkpoint.

### Config

Environment variables read once at worker startup
(`internal/workers/audit_retention.go`):

```
AUDIT_LOG_RETENTION_DAYS            default 0   (never prune in DB)
DATA_ACCESS_LOG_RETENTION_MONTHS    default 13  (1 year + 1 buffer month)
DATA_ACCESS_LOG_FUTURE_MONTHS_AHEAD default 12  (rolling pre-create)
```

`AUDIT_LOG_RETENTION_DAYS=0` is the safe default — `audit_log` is
low-volume and the WORM dump is the long-term archive, so there's no
operational reason to prune in DB unless a specific data-minimisation rule
applies. Operators can flip to any positive number; chain integrity is
preserved by the checkpoint.

### Why partition-drop, not row-DELETE, for `data_access_log`

`data_access_log` is high-volume and partitioned by month (000066). Dropping
a partition is a single metadata operation; a giant `DELETE FROM ... WHERE
created_at < ...` would generate bloat and pressure autovacuum. The
worker's job is therefore "detach + drop" old partitions, not row delete.

## WORM object archive (long-term)

The in-DB tables hold the **hot** window (above). For long-term, off-box
retention — the Art. 30 records-of-processing baseline plus the
tamper-evidence defence against a migrator-level actor described above —
the canonical store is the **signed object dump** in PR #170 (SIEM export):
a regularly-emitted, signed `.jsonl` per project / per day pushed to an
object-store bucket configured for immutability.

The bucket policy is owned by ops, not the application code. When the
bucket is provisioned (#170), it must be configured as follows on
Scaleway Object Storage (fr-par):

```
Bucket:        eurobase-audit-archive
Region:        fr-par
Versioning:    enabled       ← MUST be enabled BEFORE Object Lock
Object Lock:   enabled (Compliance mode)
Retention:     7 years per object (GDPR Art. 30 + national rules)
Lifecycle:     no Expiration rule (compliance mode would block it anyway)
ACL:           private, no public access
```

> Order matters: Scaleway requires bucket Versioning to be enabled
> **before** Object Lock can be turned on. Provision in that order.

`Compliance mode` is the strict variant: no role — including the bucket
owner — can shorten a retained object's lock before its retention date.
That is the property that makes the dump count as the off-box
tamper-evident copy referred to above and in the migration-058 / 070
headers.

The application path that emits the dump is intentionally separated from
the bucket policy: the worker only writes; the lifecycle/lock guarantees
sit with Scaleway. A reviewer of an in-DB chain break can fetch the
matching day's dump from the bucket and walk the same hash chain
externally to identify the divergence point.
