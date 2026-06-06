# Personal-Data Access Log

> GDPR Art. 30 (records of processing), Art. 32 (security of processing)
> (Tier-1 #4). How Eurobase records **who read which personal data, when** —
> the complement to the admin-action [audit log](./audit-log.md).

## Why this exists

`public.audit_log` records sensitive **admin/platform** actions (key rotation,
config changes, DSAR exports). It does **not** record routine *reads* of tenant
personal data over the SDK. GDPR Art. 30/32 expect a controller to be able to
answer "who accessed this data subject's records, and when?". `data_access_log`
closes that gap.

## What is recorded

One row per personal-data **read**, **export**, or **download**:

| Column         | Meaning |
|----------------|---------|
| `project_id`   | tenant project (FK → `projects`, `NULL` on project delete) |
| `end_user_id`  | the data subject / caller (`NULL` for service/anon access) |
| `actor_role`   | effective RLS role: `authenticated`, `service`, `anon`, or `platform` |
| `action`       | `read` \| `export` \| `download` |
| `target_table` | tenant table, `storage_objects`, or `*` (whole-account export) |
| `target_keys`  | JSON describing *which* rows/keys (`{"id":…}`, `{"filters":…}`, `{"key":…}`) |
| `ip`           | client IP (X-Forwarded-For aware) |
| `created_at`   | timestamp; also the partition key |

`target_keys` records the **query shape**, never the row *contents* — the log
says what was accessed, not the personal data itself (which would defeat the
purpose).

### Hook points

| Path | Action | Where |
|------|--------|-------|
| `GET /v1/db/{table}`        | `read`     | `internal/query/handler.go` (`handleSelectRows`) |
| `GET /v1/db/{table}/{id}`   | `read`     | `internal/query/handler.go` (`handleSelectRowByID`) |
| `GET /v1/storage/{key}`     | `download` | `internal/storage/handler.go` (`DownloadFile`) |
| `GET /v1/auth/user`         | `read`     | `internal/enduser/handler.go` (`HandleGetUser`) |
| `POST /v1/auth/me/export`   | `export`   | `internal/compliance/export_handler.go` (`HandleSelfServeExport`) |

## Design: never on the critical path

`/v1/db` reads are a hot path, so logging is **async, sampled, and batched**
(`internal/audit/access.go`):

- **Async** — `Record()` only does a non-blocking channel send and returns. A
  single background worker batches the inserts. The request never waits on a
  DB round-trip for the log write, so p99 read latency is unaffected.
- **Best-effort** — if the buffer (default 8192) is full during a burst,
  events are **dropped and counted** (a warning is logged), never queued
  unbounded and never blocking. A dropped read log is acceptable; a stalled
  request is not. The durable copy is the off-box WORM dump (#170/#171).
- **Batched** — flushed every 2 s or every 256 events, in one round-trip.
- **Sampled** — `read` actions are subject to a sample rate; `export` and
  `download` are **always** logged (they touch the most data).

### Configuration

| Env var | Default | Effect |
|---------|---------|--------|
| `AUDIT_DATA_ACCESS_ENABLED`     | `true` (on)  | set `false` to disable the recorder entirely |
| `AUDIT_DATA_ACCESS_READ_SAMPLE` | `1.0` (all)  | P(log) for `read` actions, `0`–`1`. Lower it for very high-traffic projects |

Default is full coverage (sample every read) so compliance holds out of the
box; the knob exists for projects where read volume makes 1:1 logging
expensive.

## Storage: monthly partitions (migration 000059)

`data_access_log` is **range-partitioned by month** on `created_at`:

- The retention job (#171) can **drop an old month's partition** instead of a
  giant `DELETE` — cheap, lock-light data minimisation.
- Queries that filter on `created_at` prune to a few partitions.
- `public.ensure_data_access_log_partition(month)` creates a month's partition
  idempotently (used by the pre-create loop and the rolling job in #171).
- A **`DEFAULT` partition** guarantees inserts never fail on a missing month;
  rows simply land in default and can be redistributed later.
- The migration pre-creates the current month + the next 11.

## Append-only (defence-in-depth)

Mirroring `audit_log`: the runtime `eurobase_gateway` role may `INSERT`/`SELECT`
but not `UPDATE`/`DELETE` — enforced on the parent, the `DEFAULT` partition, and
**every** monthly partition (`ensure_data_access_log_partition` strips
`UPDATE`/`DELETE` on each one it creates, because the 000037 default grant would
otherwise hand the gateway full DML). Only `eurobase_migrator` (deploy-only,
table owner) keeps `UPDATE`/`DELETE`, which the retention job needs to drop old
partitions.

Unlike `audit_log`, this stream is **not** a per-row hash chain: it is
high-volume and sampled, and the async batched writer can't hold a per-row
advisory lock without becoming a bottleneck. Tamper-evidence for this stream is
provided by the off-box signed WORM dump (#170/#171); the in-DB protection here
is append-only.

## Querying

```sql
-- Everything a data subject's records were touched by, most recent first:
SELECT created_at, actor_role, action, target_table, target_keys, ip
FROM   public.data_access_log
WHERE  project_id = $1 AND end_user_id = $2
ORDER  BY created_at DESC;
```

Indexes cover `(project_id, created_at DESC)`, `(end_user_id, created_at DESC)`,
and `(action, created_at DESC)`.
