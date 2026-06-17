# Breach Register — GDPR Art. 33 Workflow

> GDPR Art. 33 (notification to supervisory authority), Art. 33(5) (internal
> register), Art. 34 (notification to data subjects). Tier-1 launch blocker
> #4 (issue #172). The operational runbook lives at
> [`docs/runbooks/breach-notification.md`](../runbooks/breach-notification.md);
> this file documents the data model and the HTTP surface.

## What it is

`public.breach_register` (migration `000065`) is the append-only ledger the
DPO maintains under Art. 33(5) — including breaches we judged **not** to be
notifiable. It is what we hand to the supervisory authority if they audit
our process. It is also what the SLA dashboard reads to flag the
24-hour (DPA §10) and 72-hour (Art. 33) commitments.

## Data model

Every change to an incident writes a NEW row keyed by `incident_id`. The
most recent row by `seq` is the authoritative snapshot. UPDATE and DELETE
are revoked from `eurobase_gateway` and `eurobase_developer` (same pattern
as `audit_log` in 000058). A compromised gateway forging *new* appends is
caught by cross-referencing the WORM object archive shipping in #171; the
in-DB append-only constraint catches modification, deletion, and reordering
of existing rows.

| Column | Purpose |
| ------ | ------- |
| `incident_id` | Stable identifier across all rows of one incident. |
| `seq` | Monotonic per-row sequence. Latest = authoritative. |
| `project_id` | Tenant this incident concerns. NULL for platform-only. |
| `affects_platform` | True if Eurobase platform data is involved. |
| `title`, `description` | DPO summary. |
| `likely_consequences`, `measures_taken` | Art. 33(3) bullets. |
| `data_categories`, `subject_categories` | `TEXT[]` — DPO writes them in plain language. |
| `records_affected`, `subjects_affected` | Approximate counts; NULL = under investigation. |
| `occurred_at`, `occurred_until` | Best-known window of the breach itself. |
| `awareness_at` | The moment Eurobase staff first had reasonable certainty. **All SLAs anchor here.** |
| `contained_at`, `resolved_at` | Containment and full resolution timestamps. |
| `notified_authority`, `notified_authority_at` | SA filing fact. |
| `notified_customers`, `notified_customers_at` | DPA §10 customer notice fact. |
| `notified_subjects`, `notified_subjects_at` | Art. 34 direct end-user notice (only where applicable). |
| `lead_sa` | Supervisory-authority code (default `fr-cnil`). |
| `mttd_seconds` | `awareness_at − occurred_at`. |
| `mttr_seconds` | `resolved_at − awareness_at`. |
| `status` | `open` / `contained` / `notified_customers` / `notified_authority` / `closed` / `no_action`. |
| `actor_id`, `actor_email`, `note` | Who wrote the row and why. |
| `metadata` | Reserved JSONB for forward-compat. |

`no_action` covers the "logged but not notified" case the runbook calls out
— Art. 33(5) requires keeping the record either way.

## HTTP surface

All routes are platform-authenticated and admin-gated. Mounted under
`/platform/projects/{id}/compliance/breaches`.

| Method | Path | Purpose |
| ------ | ---- | ------- |
| GET | `/breaches` | List the latest snapshot per incident for the project. |
| POST | `/breaches` | Open a new incident (writes row 1). |
| GET | `/breaches/{incidentId}` | Latest snapshot + full append-only history. |
| PATCH | `/breaches/{incidentId}` | Write a new snapshot with partial changes applied. |
| POST | `/breaches/{incidentId}/close` | Terminal `closed` or `no_action`; computes MTTR. |
| POST | `/breaches/{incidentId}/subjects` | Identify the end-users in scope (count + capped sample IDs). |
| POST | `/breaches/{incidentId}/notify-customers` | Render + BCC the customer email; records the dispatch. |
| POST | `/breaches/{incidentId}/authority-form` | Render the SA paste-in. `{"filed": true}` records the SA filing. |
| GET | `/breaches/{incidentId}/sla` | Current state of the 24h / 72h clocks. |

Every write also emits an `audit_log` entry (`breach.opened` /
`breach.updated` / `breach.notified_customers` / `breach.notified_authority` /
`breach.notified_subjects` / `breach.closed` / `breach.subjects_identified`)
so a tampered register can be cross-checked against the chained audit log.

## Subject identification

`internal/breach/subjects.go` reuses the DSAR table-discovery logic from
`internal/compliance/export.go` (`DiscoverUserTables`, `ListTenantTables`)
so the breach-scope subject query stays in sync with what DSAR exports
already consider "user data". The query runs under
`edb.RunAsService` (sets `app.end_user_role='service'`) so tenant `users`
RLS does not silently zero out the count when there is no end-user actor.

Scope filters available in the request body (`SubjectQuery`):

- `created_from` / `created_until` — bounds the `users.created_at` window;
  use when the breach exposed a sign-up form or a batch import.
- `updated_from` / `updated_until` — bounds the `users.updated_at` window;
  use when the breach was a leak of "active sessions" or recently edited
  profiles.
- `user_ids` — restricts to a known list (intersected with the tenant).
- `limit` — caps the size of the `affected_ids` sample (default 100;
  `0` = no IDs returned, count only; `-1` = unbounded).

The count is always exact; `affected_ids` is for spot-checking.

## MTTD / MTTR metrics

Exposed on the private metrics endpoint:

| Metric | Help |
| ------ | ---- |
| `eurobase_breach_opened_total` | Counter — incidents opened. |
| `eurobase_breach_closed_total{status}` | Counter — labelled by terminal status. |
| `eurobase_breach_mttd_seconds` | Histogram — `awareness_at − occurred_at`. |
| `eurobase_breach_mttr_seconds` | Histogram — `resolved_at − awareness_at`. |

Buckets are chosen to make the DPA SLAs legible (5m, 30m, 1h, 4h, 12h,
**24h** [DPA §10], 48h, **72h** [Art. 33], 7d, 30d).

## Retention

Indefinite. Art. 33(5) does not set a ceiling, and the WORM archive (#171)
ensures we keep the original off-box copy beyond any operational
incident-data purge.

## Acceptance test

`scripts/breach-synthetic-drill.sh` (TODO if the runbook drill becomes
recurrent) opens a synthetic incident, identifies subjects on a seeded
tenant, renders+sends the customer template to a test mailbox, and asserts
the MTTD/MTTR metrics moved. The same flow is documented inline in
`docs/runbooks/breach-notification.md` and tracked via the pre-flight
checklist.
