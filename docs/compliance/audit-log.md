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
Audit writes are low-frequency, so the lock is not a hot path.

### Append-only enforcement
Migration 000058 runs `REVOKE UPDATE, DELETE ON public.audit_log` from
`eurobase_gateway` and `eurobase_developer`. The runtime can only `INSERT`
and `SELECT`. Only `eurobase_migrator` (deploy-only) keeps full rights, by
necessity — schema changes need it.

> An owner-level actor (migrator) could still rewrite the whole forward chain.
> The independent defence is the off-box **WORM dump** (signed, append-only
> object-store export) shipping in the SIEM-export / retention work
> (#170/#171) — once a hash is exported off-box, in-DB rewriting is detectable
> against the external copy.

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

## Testing

`internal/audit/verifier_test.go` (integration, runs in CI `test-go`):
appends entries, verifies clean, then mutates and deletes rows directly in the
DB and asserts `Verify` flags both — the content-tamper and the
linkage-break paths.

## Retention & export
1-year retention and the vendor-neutral SIEM export (webhook / syslog / signed
object dump) are documented in
[`audit-export.md`](./audit-export.md) once those PRs land (#170, #171).
