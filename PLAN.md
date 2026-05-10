# DSAR Export Implementation Plan (#89 + #90)

## Overview

Two export paths sharing a common infrastructure layer:

1. **Tenant-level export** (#89) — full project snapshot (all tables, users, storage manifest, audit log)
2. **End-user-level export** (#90) — per-user data extraction across all tenant tables

Output formats: **JSON** + **CSV** bundled in a `.zip`. No PDF in this iteration.

---

## Architecture

```
┌─────────────────┐     ┌────────────────────┐     ┌──────────────────────┐
│  API Handler    │────►│  River Job Queue   │────►│  Export Worker       │
│  (sync or async)│     │  (export_tenant /  │     │  (scans schema,     │
│                 │     │   export_user)     │     │   writes zip → S3)  │
└─────────────────┘     └────────────────────┘     └──────────────────────┘
                                                            │
                                                            ▼
                                                   ┌──────────────────┐
                                                   │  Scaleway S3     │
                                                   │  exports/ prefix │
                                                   │  (fr-par)        │
                                                   └──────────────────┘
```

Small exports (< 5MB) are returned inline as `application/zip`. Larger exports go async via River and produce a signed download URL.

---

## Step 1: Database migration (`000032_export_requests.{up,down}.sql`)

```sql
CREATE TABLE public.export_requests (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id  UUID NOT NULL REFERENCES public.projects(id) ON DELETE CASCADE,
    user_id     UUID,          -- NULL = full tenant export; non-null = per-user export
    status      TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','running','completed','failed')),
    format      TEXT NOT NULL DEFAULT 'json' CHECK (format IN ('json','csv')),
    s3_key      TEXT,
    file_size   BIGINT,
    error       TEXT,
    requested_by UUID NOT NULL, -- platform_user or end_user ID
    requested_by_type TEXT NOT NULL DEFAULT 'platform' CHECK (requested_by_type IN ('platform','enduser')),
    started_at  TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    expires_at  TIMESTAMPTZ,   -- signed URL expiry / cleanup deadline
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_export_requests_project ON public.export_requests(project_id, created_at DESC);
CREATE INDEX idx_export_requests_status ON public.export_requests(status) WHERE status IN ('pending','running');
```

---

## Step 2: Export service (`internal/compliance/export.go`)

Core service struct and methods:

```go
type ExportService struct {
    pool     *pgxpool.Pool
    s3       *storage.S3Client
    auditSvc *audit.Service
}

// RequestTenantExport creates an export_request, enqueues the job, returns the request ID.
func (s *ExportService) RequestTenantExport(ctx, projectID, requestedBy, format) (*ExportRequest, error)

// RequestUserExport creates an export_request for a specific end-user.
func (s *ExportService) RequestUserExport(ctx, projectID, userID, requestedBy, requestedByType, format) (*ExportRequest, error)

// GetExportStatus returns current status + download URL (if completed).
func (s *ExportService) GetExportStatus(ctx, exportID, projectID) (*ExportRequest, error)

// ListExports returns paginated export history for a project.
func (s *ExportService) ListExports(ctx, projectID, limit, offset) ([]ExportRequest, error)
```

---

## Step 3: Export worker (`internal/workers/export.go`)

Two River job types sharing core logic:

```go
// Job args (in internal/jobs/args.go)
type TenantExportArgs struct { ExportID string; ProjectID string; Format string }
type UserExportArgs   struct { ExportID string; ProjectID string; UserID string; Format string }

func (TenantExportArgs) Kind() string { return "export_tenant" }
func (UserExportArgs)   Kind() string { return "export_user" }
```

### Worker logic (shared helper `executeExport`):

1. Update `export_requests.status = 'running'`, set `started_at`
2. Resolve tenant schema name from project_id
3. **Collect data:**
   - **Tenant export:** `SELECT * FROM {schema}.{table}` for every user-visible table + users table + storage manifest + audit log
   - **User export:** For each table with a `user_id` column (or FK → users.id), `SELECT * WHERE user_id = $1`. Also: user profile row, storage objects with matching prefix, auth-related audit entries.
4. **Write zip** to an in-memory buffer (or temp file if > threshold):
   - `_metadata.json` — project info, export timestamp, format version
   - `_user_profile.json` (user export only) — full user record
   - `tables/{table_name}.json` or `tables/{table_name}.csv` — per-table data
   - `_storage_manifest.json` — list of objects (key, size, content_type)
   - `_audit_log.json` — relevant audit entries
5. **Upload** zip to S3: `exports/{project_id}/{export_id}.zip`
6. Update `export_requests`: status=completed, s3_key, file_size, expires_at (now+7d)
7. Log `audit.ActionDataExported` with metadata (type, user_id if applicable, size)

### Table discovery for user export:

```go
func discoverUserTables(ctx, pool, schemaName) ([]TableRef, error)
// Query information_schema.columns WHERE column_name = 'user_id'
// OR query pg_constraint for FKs referencing {schema}.users(id)
// Returns list of (table_name, user_column_name) pairs
```

---

## Step 4: API handlers (`internal/compliance/export_handler.go`)

### Platform routes (developer/admin access):

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| POST | `/platform/projects/{id}/compliance/export` | platform JWT | Request tenant-level export |
| POST | `/platform/projects/{id}/compliance/user-export` | platform JWT | Request per-user export (body: `{"user_id":"...","format":"json"}`) |
| GET | `/platform/projects/{id}/compliance/exports` | platform JWT | List export history |
| GET | `/platform/projects/{id}/compliance/exports/{exportId}` | platform JWT | Get status + download URL |

### SDK self-serve route (end-user access):

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| POST | `/v1/auth/me/export` | API key + end-user JWT | End-user triggers own data export |
| GET | `/v1/auth/me/export/{exportId}` | API key + end-user JWT | Check status / download |

### Rate limits:
- Tenant export: 1 per project per hour
- User export: 1 per user per 24 hours

---

## Step 5: Wire into router (`internal/gateway/router.go`)

Mount under existing `/platform/projects/{id}` group (already has platformAuth + audit context):

```go
// Inside the /platform/projects/{id} route group:
exportSvc := compliance.NewExportService(pool, s3Client, auditSvc)
r.Post("/compliance/export", compliance.HandleRequestTenantExport(exportSvc))
r.Post("/compliance/user-export", compliance.HandleRequestUserExport(exportSvc))
r.Get("/compliance/exports", compliance.HandleListExports(exportSvc))
r.Get("/compliance/exports/{exportId}", compliance.HandleGetExport(exportSvc, s3Client))

// Inside the /v1/auth group (after endUserMw):
r.Post("/me/export", compliance.HandleSelfServeExport(exportSvc))
r.Get("/me/export/{exportId}", compliance.HandleSelfServeExportStatus(exportSvc, s3Client))
```

---

## Step 6: Register worker (`cmd/worker/main.go`)

```go
river.AddWorker(riverWorkers, &workers.TenantExportWorker{DBPool: pool, S3: s3Client, AuditSvc: auditSvc})
river.AddWorker(riverWorkers, &workers.UserExportWorker{DBPool: pool, S3: s3Client, AuditSvc: auditSvc})
```

---

## Step 7: Metrics (`internal/metrics/metrics.go`)

Add to existing registry:
- `eurobase_exports_total{type="tenant|user",status="completed|failed"}` — counter
- `eurobase_export_duration_seconds{type="tenant|user"}` — histogram
- `eurobase_export_size_bytes{type="tenant|user"}` — histogram

---

## Step 8: Background cleanup job

Add to existing `StartLogCleanup`-style ticker (or new goroutine in main.go):
- Every hour: `DELETE FROM export_requests WHERE expires_at < now()` + delete corresponding S3 objects
- Retention: 7 days for download URL, 30 days for the S3 object itself

---

## Step 9: Tests

- **Unit tests** (`internal/compliance/export_test.go`):
  - `discoverUserTables` with mock schema
  - JSON/CSV serialization of table data
  - Rate limit enforcement
- **Integration test** (requires DB):
  - Create tenant schema with sample tables
  - Insert user rows
  - Run tenant export → verify zip contents
  - Run user export → verify only user's rows included
  - Verify audit log entry written
- **API test** (Postman collection `docs/eurobase-dsar-export.postman_collection.json`):
  - POST export → 202
  - GET status → pending → completed
  - Download URL → valid zip
  - Rate limit → 429
  - Self-serve → only own data

---

## Step 10: Console UI updates

### Compliance page (`/p/{id}/compliance`)
- New "Data Export" section below DPA report
- "Export full project data" button (format dropdown: JSON/CSV)
- Export history table: status, requested_by, size, created_at, download link
- Per-user export via user management page (`/p/{id}/users` → action menu → "Export user data")

---

## File summary (new files)

| File | Purpose |
|------|---------|
| `migrations/000032_export_requests.up.sql` | Export requests table |
| `migrations/000032_export_requests.down.sql` | Rollback |
| `internal/compliance/export.go` | ExportService (request, status, list) |
| `internal/compliance/export_handler.go` | HTTP handlers (platform + SDK) |
| `internal/compliance/export_test.go` | Unit tests |
| `internal/workers/export.go` | TenantExportWorker + UserExportWorker |
| `internal/jobs/args.go` | Add TenantExportArgs + UserExportArgs |
| `docs/eurobase-dsar-export.postman_collection.json` | API test collection |

## File summary (modified)

| File | Change |
|------|--------|
| `internal/gateway/router.go` | Mount export routes |
| `internal/metrics/metrics.go` | Add export counters |
| `cmd/worker/main.go` | Register export workers |
| `console/src/routes/(app)/p/[id]/compliance/+page.svelte` | Export UI |
| `console/src/routes/(app)/p/[id]/users/+page.svelte` | "Export user data" action |

---

## Sovereignty compliance

- All data stays in Scaleway S3 (fr-par)
- Signed URLs route through `s3.fr-par.scw.cloud`
- No external PDF renderer or third-party SaaS in the path
- Export cannot be plan-gated (GDPR right applies regardless)

---

## Implementation order

1. Migration → 2. ExportService → 3. Workers → 4. Handlers → 5. Router wiring → 6. Metrics → 7. Cleanup job → 8. Tests → 9. Console UI → 10. Postman collection
