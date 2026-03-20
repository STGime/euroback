# EUROBASE — MVP Implementation Plan

**API Gateway · Console Dashboard · Provisioning Orchestrator**

Technical Specification for Development Team

Version 1.1 — March 2026 — Confidential

---

## Table of Contents

1. [Overview and Guiding Principles](#1-overview-and-guiding-principles)
2. [Technology Stack](#2-technology-stack)
3. [Repository and Project Structure](#3-repository-and-project-structure)
4. [API Gateway — Detailed Specification](#4-api-gateway--detailed-specification)
5. [Console Dashboard — Detailed Specification](#5-console-dashboard--detailed-specification)
6. [Provisioning Orchestrator — Detailed Specification](#6-provisioning-orchestrator--detailed-specification)
7. [MVP Onboarding Flow — Step-by-Step Design](#7-mvp-onboarding-flow--step-by-step-design)
8. [Data Models and Database Schema](#8-data-models-and-database-schema)
9. [API Contract — Endpoint Reference](#9-api-contract--endpoint-reference)
10. [Extension Architecture — Future-Proofing](#10-extension-architecture--future-proofing)
11. [Development Phases and Sprint Plan](#11-development-phases-and-sprint-plan)
12. [Testing Strategy](#12-testing-strategy)
13. [Deployment and CI/CD](#13-deployment-and-cicd)
14. [Open Questions and Decisions](#14-open-questions-and-decisions)

---

## 1. Overview and Guiding Principles

Eurobase is an EU-native Backend-as-a-Service (BaaS) platform. The MVP delivers three core services: authentication (via Hanko), managed PostgreSQL database, and S3-compatible object storage (both via Scaleway). The platform is accessed through a web console and client SDKs.

### 1.1 MVP Scope

The MVP enables a developer to: (a) create a Eurobase project via a guided onboarding flow, (b) get provisioned with a dedicated database schema and S3 bucket, (c) authenticate end-users via Hanko-powered auth, (d) read/write data through a REST API with automatic tenant isolation, (e) upload/download files to their isolated storage bucket, and (f) manage all of the above from a console dashboard.

### 1.2 Guiding Principles

- **EU sovereignty first:** every component runs on EU-incorporated infrastructure, all data stays within the EU.
- **Convention over configuration:** sensible defaults that work out of the box, with override capability for advanced users.
- **Extension-ready architecture:** the MVP ships with 3 services but the internal architecture uses a plugin/provider model that accommodates future services (push notifications, edge functions, vector search, etc.) without refactoring.
- **Developer experience above all:** if a developer cannot go from signup to a working backend in under 5 minutes, the onboarding has failed.
- **Defense-in-depth isolation:** schema-per-tenant + RLS + scoped bucket credentials. Never rely on a single isolation layer.

### 1.3 Non-Goals for MVP

- GraphQL API (REST-first for MVP, GraphQL in v1.1).
- Custom domains per project (infrastructure complexity deferred to v1.1).
- Edge functions / serverless compute (planned for v1.2).
- Multi-region replication (single Scaleway region for MVP).
- Mobile-native SDKs (JavaScript/TypeScript SDK only for MVP).

---

## 2. Technology Stack

| Layer | Technology | Rationale |
|-------|-----------|-----------|
| **API Gateway** | Go (Golang) 1.22+ | Fastest cold start, minimal memory, excellent concurrency. Strong ecosystem for HTTP proxies (chi, echo). Single binary deployment. |
| **Console UI** | SvelteKit + TypeScript | Lightweight, fast SSR/SPA hybrid. Excellent DX. Smaller bundle than React/Next. Good for dashboard UIs. Deploys as static site or SSR. |
| **Client SDK** | TypeScript (isomorphic) | Works in browser + Node.js. Published to npm. Future SDKs (Dart, Swift, Kotlin) follow same API shape. |
| **Provisioning** | Go (shared codebase) | Same language as gateway. Runs as background workers/jobs. Uses Scaleway SDK for Go. |
| **Database** | Scaleway PostgreSQL 16 | Managed, HA, PITR. EU datacenters. pgvector for future AI features. |
| **Object Storage** | Scaleway Object Storage | S3-compatible. No API fees. Same VPC as DB. |
| **Auth Provider** | Hanko Cloud | German GmbH. Passkey-first. $0.01/MAU. Web components. |
| **Cache / Realtime** | Scaleway Redis 7 | Session cache, rate limiting, pub/sub for WebSocket fan-out. |
| **Task Queue** | River (Go + PostgreSQL) | No extra infra needed. Job queue backed by PostgreSQL. Handles provisioning, webhooks, cleanup. |
| **Container Orchestration** | Scaleway Kapsule (K8s) | Managed Kubernetes in Paris. Same VPC as all data services. |
| **CI/CD** | GitHub Actions | Build, test, push image to Scaleway Container Registry, deploy to Kapsule. |
| **Monitoring** | Scaleway Cockpit (Grafana) | Metrics, logs, alerts. No external dependency. |
| **Transactional Email** | Scaleway TEM | EU-native. For onboarding emails, alerts, magic links. |
| **Billing / Payments** | Mollie (Netherlands) | EU-incorporated (Amsterdam, NL). SEPA direct debit, cards, iDEAL. No monthly fees. Full EU data sovereignty — zero US data transfer. Subscriptions API for recurring billing. |

---

## 3. Repository and Project Structure

Monorepo structure using Go workspaces for backend and a separate SvelteKit app for the console. This keeps deployment independent while sharing types and constants.

```
eurobase/
├── apps/
│   ├── gateway/          # Go — API gateway binary
│   ├── console/          # SvelteKit — dashboard UI
│   └── worker/           # Go — background job processor
├── pkg/
│   ├── tenant/           # Tenant lifecycle (provision, migrate, deprovision)
│   ├── auth/             # Hanko JWT validation, middleware
│   ├── database/         # PostgreSQL pool, schema routing, RLS
│   ├── storage/          # S3 client, bucket operations, signed URLs
│   ├── realtime/         # WebSocket hub, Redis pub/sub bridge
│   ├── ratelimit/        # Token bucket via Redis
│   ├── billing/          # Mollie integration, usage metering, plan enforcement
│   ├── mollie/           # Mollie API client, webhook handlers, subscription mgmt
│   ├── webhook/          # Event dispatch, retry logic
│   └── provider/         # Extension interface (future services)
├── sdk/
│   └── js/               # TypeScript SDK (npm package)
├── migrations/           # Platform-level SQL migrations (public schema)
├── deploy/
│   ├── k8s/              # Kapsule manifests (gateway, worker, ingress)
│   ├── terraform/        # Scaleway infra (DB, S3, Redis, Kapsule, DNS)
│   └── docker/           # Dockerfiles for gateway and worker
└── docs/                 # Architecture decision records, API docs
```

---

## 4. API Gateway — Detailed Specification

The API gateway is the central runtime of Eurobase. It accepts all inbound HTTP requests, authenticates them, routes them to the correct tenant context, executes the requested operation against PostgreSQL or S3, and returns the response. It is a single Go binary running as a Kubernetes deployment with horizontal pod autoscaling.

### 4.1 Gateway Architecture

The gateway is structured as a middleware pipeline using the chi router. Each request passes through a chain of middleware functions before reaching the handler. The pipeline is designed so that the tenant context is established before any data operation occurs.

#### Request Pipeline

1. TLS termination (handled by Kapsule ingress / load balancer)
2. CORS middleware (configurable per project)
3. Rate limiter middleware (token bucket via Redis, per-project limits)
4. Auth middleware (validates Hanko JWT, extracts tenant_id + user_id + role from claims)
5. Tenant context middleware (calls `set_tenant_context()` on the DB connection, resolves S3 bucket name)
6. Request router (dispatches to the correct handler based on path)
7. Handler (executes the business logic: query, insert, upload, etc.)
8. Response serialization (JSON, with optional compression)

### 4.2 Module Breakdown

#### 4.2.1 Auth Middleware (`pkg/auth/`)

**Responsibility:** Validate Hanko JWTs on every request. Extract claims. Block unauthenticated access to data endpoints.

Implementation details:

- Fetch Hanko JWKS (JSON Web Key Set) on startup and cache it. Refresh every 60 minutes or on cache miss.
- Validate JWT signature using the JWKS. Check `exp`, `iss` (must be Hanko), `aud` (must match project ID).
- Extract custom claims: `tenant_id` (the Eurobase project ID), `sub` (Hanko user ID), `email`, `role`.
- Inject claims into the Go request context: `ctx = context.WithValue(ctx, "tenant", tenantClaims)`.
- Public endpoints (health check, docs, onboarding) bypass auth middleware via route-level configuration.
- Support both `Authorization: Bearer <token>` header and `eb-api-key: <key>` for server-to-server calls.

#### 4.2.2 Tenant Router (`pkg/tenant/`)

**Responsibility:** Resolve the tenant from the JWT claims, set the database search_path and session variables, scope S3 operations to the tenant bucket.

Implementation details:

- Look up `tenant_id` in the platform tenants table (cached in Redis with 60s TTL).
- Verify tenant is not suspended or over quota. Return 403 with clear error if so.
- Acquire a connection from the PostgreSQL pool and execute: `SELECT set_tenant_context($1)`.
- Store the scoped DB connection and S3 bucket name in the request context for downstream handlers.
- On request completion, the connection is returned to the pool. The transaction-local session variables auto-reset.

#### 4.2.3 Query Engine (`pkg/database/`)

**Responsibility:** Translate REST API calls into PostgreSQL queries. Handle CRUD, filtering, sorting, pagination, and relationships.

The query engine is the most complex module. It provides a Supabase-like REST API where the URL path maps to table names and query parameters control filtering.

**REST API Pattern:**

```
GET    /v1/db/{table}              → SELECT with filters
GET    /v1/db/{table}/{id}         → SELECT by primary key
POST   /v1/db/{table}              → INSERT (single or batch)
PATCH  /v1/db/{table}/{id}         → UPDATE by primary key
DELETE /v1/db/{table}/{id}         → DELETE by primary key
POST   /v1/db/rpc/{function}       → Call a PostgreSQL function
```

**Query Parameters:**

```
?select=id,name,email             → Column selection
?order=created_at.desc            → Sorting
?limit=20&offset=40               → Pagination
?name=eq.Stefan                   → Exact match filter
?age=gt.25                        → Greater than
?status=in.(active,pending)       → IN clause
?or=(age.gt.25,name.eq.Stefan)    → OR conditions
```

The query engine parses these parameters and builds parameterized SQL queries. All user input is parameterized (never concatenated) to prevent SQL injection. The query is executed against the tenant's schema (set by the tenant router).

**Security Constraints:**

- Table names are validated against an allowlist: the tenant's actual tables in their schema. If a table does not exist, return 404.
- Column names are validated against `pg_catalog` for the resolved table. Invalid columns return 400.
- Query complexity limits: max 1000 rows per response, max 5 JOINs, max 10 filter conditions. Configurable per plan.
- Rate limiting: per-project, per-table, configurable. Default: 100 req/sec for free tier, 1000 for pro.
- All writes emit an event to the webhook/realtime system (see 4.2.5 and 4.2.6).

#### 4.2.4 Storage Proxy (`pkg/storage/`)

**Responsibility:** Handle file uploads and downloads, generate pre-signed URLs, manage storage object metadata.

**Endpoints:**

```
POST   /v1/storage/upload          → Upload file (multipart/form-data)
GET    /v1/storage/{key}           → Download file (or redirect to signed URL)
DELETE /v1/storage/{key}           → Delete file
GET    /v1/storage                 → List files (with prefix/pagination)
POST   /v1/storage/signed-url      → Generate pre-signed upload/download URL
```

Implementation details:

- Uploads are streamed directly to Scaleway S3 using the aws-sdk-go-v2 S3 client. The gateway does not buffer the full file in memory.
- Each upload is stored at: `s3://eb-{tenant_id}/{user_provided_path}`. The bucket is scoped by the tenant router.
- File metadata (key, content_type, size_bytes, uploaded_by) is written to the tenant's `storage_objects` table for queryability.
- Pre-signed URLs allow the client SDK to upload directly to S3, bypassing the gateway for large files. The signed URL is scoped to the tenant's bucket and a specific key prefix.
- File size limits: 50MB via gateway upload, 5GB via pre-signed URL. Configurable per plan.
- MIME type validation: optional allowlist per project (e.g., only images, only PDFs).
- Image transformation (resize, crop, format conversion) is a v1.1 feature. The storage proxy is designed to accept a transform pipeline via query params (e.g., `?width=200&format=webp`) but the MVP returns the original file.

#### 4.2.5 Realtime Engine (`pkg/realtime/`)

**Responsibility:** Push database changes to connected clients via WebSocket or Server-Sent Events (SSE).

Implementation details:

- Clients connect to `/v1/realtime?token={jwt}` and subscribe to channels: `db:{table}`, `db:{table}:{id}`, `storage:{bucket}`.
- When the query engine writes to the database, it publishes a change event to Redis pub/sub: `channel = tenant:{tenant_id}:{table}`, `payload = {type: INSERT|UPDATE|DELETE, record: {...}, old_record: {...}}`.
- The realtime engine subscribes to Redis and fans out events to connected WebSocket clients that have subscribed to the matching channel.
- Authentication is validated on WebSocket upgrade. Tenant context is resolved and only events for the authenticated tenant are forwarded.
- MVP supports WebSocket only. SSE is a v1.1 addition for environments where WebSocket is blocked.
- Connection limits: 100 concurrent connections per project (free), 10,000 (pro). Enforced by the realtime engine via a per-tenant counter in Redis.

#### 4.2.6 Webhook Dispatch (`pkg/webhook/`)

**Responsibility:** Deliver event notifications to customer-configured HTTP endpoints.

Implementation details:

- Customers configure webhooks via the console: URL, events to listen for (e.g., `db.insert.users`, `storage.upload`), and a signing secret.
- When a matching event occurs, the webhook dispatcher enqueues a delivery job in River (PostgreSQL-backed job queue).
- The worker process picks up the job, sends an HTTP POST to the webhook URL with the event payload, signed with HMAC-SHA256 using the customer's secret.
- Retry policy: 3 attempts with exponential backoff (10s, 60s, 300s). Failed deliveries are logged and visible in the console.
- Webhook configuration is stored in the platform database (`public.webhooks` table), not in the tenant schema.

### 4.3 Gateway Configuration

The gateway is configured via environment variables (12-factor app). All secrets are stored in Scaleway Secret Manager and injected into the Kubernetes pod at runtime.

| Variable | Example | Description |
|----------|---------|-------------|
| `DATABASE_URL` | `postgres://eurobase_api:...@...:5432/eurobase` | Scaleway managed PG connection string |
| `REDIS_URL` | `redis://...:6379` | Scaleway managed Redis |
| `S3_ENDPOINT` | `https://s3.fr-par.scw.cloud` | Scaleway S3 endpoint |
| `S3_ACCESS_KEY` / `S3_SECRET_KEY` | `SCW...` | Scaleway API keys for S3 |
| `HANKO_API_URL` | `https://api.hanko.io/<project>` | Hanko project API for JWKS |
| `SCW_PROJECT_ID` | `xxxxxxxx-xxxx-...` | Scaleway project for provisioning API |
| `GATEWAY_PORT` | `8080` | HTTP listen port |
| `MOLLIE_API_KEY` | `live_xxxxxxxx` | Mollie API key (stored in Scaleway Secret Manager) |
| `MOLLIE_WEBHOOK_BASE_URL` | `https://api.eurobase.eu` | Base URL for Mollie webhook callbacks |
| `LOG_LEVEL` | `info` | debug / info / warn / error |

---

## 5. Console Dashboard — Detailed Specification

The console is a SvelteKit web application that serves as the management interface for Eurobase projects. It communicates with the API gateway for all data operations and with dedicated platform management endpoints for project-level operations (provisioning, settings, billing).

### 5.1 Page Map

| Route | Page Name | Description |
|-------|-----------|-------------|
| `/` | Landing / Marketing | Public page with value proposition, pricing, and sign-up CTA |
| `/signup` | Sign Up | Hanko Elements registration component. Creates platform account. |
| `/login` | Login | Hanko Elements login component. Passkey + email. |
| `/onboarding` | Onboarding Wizard | 3-step guided project setup (see Section 7 for full design) |
| `/projects` | Project List | Dashboard showing all user's projects with status, region, plan |
| `/p/{id}` | Project Overview | Summary dashboard: usage stats, recent activity, quick actions |
| `/p/{id}/auth` | Authentication | Hanko project config: auth methods, social providers, email templates |
| `/p/{id}/database` | Database | Table browser, schema editor, SQL runner, migration history |
| `/p/{id}/database/tables/{t}` | Table View | Spreadsheet-like view of table data. Inline editing. Filter/sort. |
| `/p/{id}/storage` | Storage Browser | File manager UI: upload, browse, preview, delete. Folder view. |
| `/p/{id}/api` | API Explorer | Interactive API docs. Auto-generated from schema. Try-it-now panel. |
| `/p/{id}/webhooks` | Webhooks | Configure webhook endpoints. View delivery logs. |
| `/p/{id}/logs` | Logs | Request logs, error logs, query performance. Filterable timeline. |
| `/p/{id}/settings` | Project Settings | Project name, region, plan, API keys, danger zone (delete project) |
| `/account` | Account Settings | Profile, billing (Mollie), team management, platform API keys |

### 5.2 Key UI Components

#### 5.2.1 Table Browser / Schema Editor

The database page is the most complex UI in the console. It must provide:

- **Table list sidebar:** shows all tables in the tenant's schema with row counts. Click to open. 'New Table' button at top.
- **Schema editor:** visual table creation. Define columns with name, type (dropdown: text, integer, boolean, uuid, timestamp, jsonb, etc.), nullable, default, primary key, references. The editor generates and executes CREATE TABLE SQL via the gateway.
- **Table data view:** spreadsheet-like grid showing rows. Columns are sortable and filterable. Inline cell editing with type-appropriate inputs (text field, checkbox for boolean, date picker for timestamp, JSON editor for jsonb). Pagination with configurable page size.
- **SQL runner:** raw SQL editor with syntax highlighting (CodeMirror/Monaco). Execute button. Results table below. Only SELECT is allowed in the SQL runner for safety; DDL must go through the schema editor.
- **Migration history:** chronological list of schema changes with timestamps and the SQL that was executed. Allows rollback by generating reverse migration SQL (approval required).

#### 5.2.2 Storage Browser

A file manager interface for the tenant's S3 bucket:

- **Folder-style navigation:** display object keys as a virtual folder hierarchy. Breadcrumb navigation.
- **Drag-and-drop upload:** files are uploaded via pre-signed URL for performance. Progress bar per file. Batch upload support.
- **Preview panel:** click a file to see a preview sidebar. Images render inline. PDFs show first page. Text files show content. Other types show metadata only.
- **Actions:** copy public URL, generate signed URL (with TTL selector), download, delete, move/rename.
- **Usage meter:** shows current storage consumption vs. plan limit as a progress bar.

#### 5.2.3 API Explorer

Auto-generated interactive API documentation:

- Reads the tenant's schema (tables, columns, types) and generates a REST API reference.
- Each endpoint has a 'Try it' panel: select method, fill in parameters, click send, see response.
- Shows the equivalent SDK code (JavaScript) for every request, for easy copy-paste.
- Displays API key and project URL prominently with copy buttons.

### 5.3 Console Authentication

The console uses Hanko Elements for its own authentication. This is a separate Hanko project from the per-tenant auth. When a user logs in to the console, they get a platform-level JWT that identifies them as a Eurobase platform user. This JWT is used to call platform management endpoints on the gateway (project CRUD, settings, billing). When the user navigates to a specific project, the console also holds the project-level context for data operations.

---

## 6. Provisioning Orchestrator — Detailed Specification

The provisioning orchestrator handles the full lifecycle of Eurobase projects: creation, scaling, suspension, and deletion. It runs as a background worker process (the `worker` binary) that processes jobs from the River queue. Some provisioning steps are synchronous (fast enough to execute during the API request) and some are asynchronous (queued for the worker).

### 6.1 Project Creation Flow

When a user completes the onboarding wizard and clicks 'Create Project', the following sequence executes:

**Synchronous (within the API request, <2 seconds):**

1. Validate project name and slug. Generate a unique project ID (UUID).
2. Insert a row into `public.projects` with `status = 'provisioning'`.
3. Create the PostgreSQL schema by calling `provision_tenant(project_id, display_name, plan)`. This creates the schema, tables, RLS policies, and grants. This is fast (~200ms) and runs in the same transaction.
4. Return the project ID and API keys to the client. The console shows a 'Setting up your project...' screen.

**Asynchronous (via River job queue, <30 seconds):**

5. Create the S3 bucket: `PUT /eb-{project_id}` via Scaleway S3 API. Set bucket policy to private.
6. Configure Hanko: Create a Hanko API key scoped to this project (or configure a tenant within the Eurobase Hanko project, depending on Hanko's multi-tenancy model).
7. Generate per-project API keys: a public key (safe for client-side use, read-only) and a secret key (server-side, full access). Store hashed in `public.api_keys`.
8. Create Mollie customer: `POST /v2/customers` with the user's email and project name. Store the Mollie customer ID in `public.platform_users.mollie_customer_id`. If the user is upgrading to a paid plan, create a Mollie Subscription with the selected plan's amount and interval.
9. Send welcome email via Scaleway TEM: includes project URL, API keys, and link to quickstart docs.
10. Update `public.projects` set `status = 'active'`. The console polls for this status change and transitions to the project dashboard.

**Error Handling:**

If any asynchronous step fails, the job is retried up to 3 times. If all retries fail, the project status is set to `provisioning_failed` and the user sees an error in the console with a 'Retry' button that re-enqueues the failed step. A platform alert is sent to the Eurobase ops team via Cockpit/Scaleway alerting.

### 6.2 Project Deletion Flow

1. User clicks 'Delete Project' in settings. Must type the project name to confirm.
2. API sets project status to `deleting`. All data API requests return 410 Gone.
3. Worker job: cancel any active Mollie Subscription for the project. Mollie handles proration automatically.
4. Worker job: export tenant data to a temporary S3 object (available for 30 days for recovery).
5. Worker job: `DROP SCHEMA {tenant_schema} CASCADE`.
6. Worker job: delete all objects in the S3 bucket, then delete the bucket.
7. Worker job: revoke Hanko API key / deactivate tenant in Hanko.
8. Worker job: delete from `public.projects`. Send confirmation email.

### 6.3 Plan Enforcement

| Limit | Free | Pro | Enterprise | Enforced By |
|-------|------|-----|-----------|-------------|
| Database rows | 50,000 | 5,000,000 | Unlimited | Gateway |
| Storage | 500 MB | 50 GB | Custom | Gateway |
| API requests/day | 10,000 | 1,000,000 | Unlimited | Rate limiter |
| Realtime connections | 100 | 10,000 | Custom | Realtime engine |
| Webhooks | 5 | 50 | Unlimited | Webhook module |
| MAU (auth) | 10,000 (Hanko free) | Included | Included | Hanko |

### 6.4 Mollie Billing Integration (`pkg/billing/`)

Eurobase uses Mollie (Amsterdam, NL) as its payment provider, ensuring the entire stack remains under EU jurisdiction with zero data transfer to the US. The billing module handles subscription lifecycle, payment processing, and usage-based invoicing.

#### Mollie API Integration

- **Mollie Go client:** use the official `mollie-api-go` SDK or a thin wrapper around Mollie's REST API v2 (`https://api.mollie.com/v2/`).
- **Authentication:** Mollie API key stored in Scaleway Secret Manager, injected as `MOLLIE_API_KEY` environment variable.
- All Mollie API calls originate from the gateway/worker within the Scaleway Paris VPC. Mollie processes payments within the EU.

#### Subscription Lifecycle

- **Free tier:** No payment method required. User can create projects immediately. Mollie customer is created but no subscription.
- **Upgrade to Pro:** Console presents a payment method form using Mollie Components (PCI-compliant embedded form). User enters card or authorizes SEPA direct debit mandate. Mollie creates a Mandate and a Subscription (monthly or yearly).
- **Recurring charges:** Mollie automatically charges the mandate on the billing cycle. Eurobase receives a webhook (`payment.paid` or `payment.failed`) and updates the project status accordingly.
- **Downgrade to Free:** Cancel the Mollie Subscription. Mollie stops future charges. Enforce free-tier limits on the next billing cycle.
- **Dunning flow:** On `payment.failed`, retry is handled by Mollie (up to 3 attempts). If all fail, Eurobase receives `subscription.cancelled` webhook. Project enters `payment_overdue` status. After 14 days grace period, project is suspended.

#### Mollie Webhook Endpoints

```
POST /platform/webhooks/mollie/payment       → Handles payment.paid, payment.failed
POST /platform/webhooks/mollie/subscription   → Handles subscription.cancelled, subscription.updated
```

Webhook payloads are verified by re-fetching the payment/subscription from Mollie's API using the ID in the webhook body (Mollie's recommended verification pattern, instead of signature verification).

#### Platform Database Tables for Billing

- `public.platform_users`: add `mollie_customer_id` (TEXT) column.
- `public.subscriptions`: `project_id`, `mollie_subscription_id`, `plan`, `status` (active|cancelled|overdue), `current_period_start`, `current_period_end`, `created_at`.
- `public.invoices`: `project_id`, `mollie_payment_id`, `amount_cents`, `currency`, `status` (paid|failed|refunded), `paid_at`, `created_at`.

#### Pricing Model (configurable in `public.plans`)

| Plan | Monthly | Yearly | Payment Methods |
|------|---------|--------|----------------|
| Free | €0 | €0 | No payment required |
| Pro | €29 | €290 (€24.17/mo) | SEPA Direct Debit, Credit/Debit Card, iDEAL, Bancontact |
| Enterprise | Custom | Custom | SEPA Direct Debit, Invoice (NET 30) |

Mollie transaction fees are absorbed by Eurobase (not passed to customers). Estimated Mollie cost per Pro subscriber: ~€0.25–0.35/month for SEPA or ~€0.83–1.07/month for card payments (2.90% + €0.25 on €29).

---

## 7. MVP Onboarding Flow — Step-by-Step Design

The onboarding flow is the first experience a developer has with Eurobase. It must be simple, informative, and fast. The target is: sign up to working backend in under 5 minutes. The flow consists of 3 steps with clear explanations at each stage.

### 7.1 Pre-Onboarding: Sign Up

The user arrives at eurobase.eu (or .dev), sees the landing page, and clicks 'Get Started Free'. They are directed to `/signup` where a Hanko Elements registration component handles account creation via passkey or email. Upon successful registration, they are redirected to `/onboarding`.

### 7.2 Step 1: Create Your Project

**Screen title:** "Name your project"

**Explanation text:** "A project is your backend. It contains a database, file storage, and authentication — everything your app needs. You can create multiple projects for different apps or environments."

**Form fields:**

- **Project name** (text input, e.g., 'My Awesome App'). Auto-generates a slug (e.g., 'my-awesome-app') which becomes the API subdomain.
- **Region selector** (dropdown with single option for MVP: 'EU West — Paris, France'). Shows a small EU flag and a note: 'All data is stored exclusively in EU datacenters under EU law.'
- **Plan selector** (card selector): Free tier is pre-selected with a summary of limits. Pro tier shown as an alternative. 'You can upgrade anytime.'

**CTA button:** "Create Project" (primary blue button)

**Background visual:** A simple animation showing the three services (auth, database, storage) connecting together as the user fills in the form.

### 7.3 Step 2: Set Up Authentication

**Screen title:** "Set up authentication for your users"

**Explanation text:** "Eurobase uses Hanko for authentication — a privacy-first, EU-based auth provider. Your users can sign in with passkeys (like FaceID or fingerprint), email magic links, or traditional passwords. You can customize this later."

**Interactive configuration:**

- **Auth method toggles** (with visual previews): Passkeys (recommended, on by default), Email + Password (on by default), Social Login (off, with 'Coming soon: Google, GitHub, Apple' labels).
- **Live preview panel** on the right side showing what the login form will look like to end-users, updating in real-time as toggles change.
- **Allowed redirect URLs input:** pre-filled with `http://localhost:3000` for development. Explanation: 'Add your app's URLs here. After login, users will be redirected back to your app.'

**CTA button:** "Continue" (proceeds to step 3)

**Skip option:** "Use defaults and continue" (for users who want speed)

### 7.4 Step 3: Explore Your Backend

**Screen title:** "Your backend is ready!"

**Explanation text:** "Your database, storage, and auth are live. Here's how to connect your app."

This screen shows three tabbed sections:

#### Tab 1: Quick Start Code

A code snippet panel showing how to initialize the Eurobase JS SDK:

```javascript
npm install @eurobase/sdk

import { createClient } from '@eurobase/sdk'

const eb = createClient({
  url: 'https://my-awesome-app.eurobase.eu',
  apiKey: 'eb_pk_xxxxxxxxxxxxxxxx'
})

// Query your database
const { data, error } = await eb.db.from('todos').select('*')

// Upload a file
await eb.storage.upload('avatars/me.jpg', file)

// Listen for realtime changes
eb.realtime.on('todos', 'INSERT', (payload) => {
  console.log('New todo:', payload.record)
})
```

#### Tab 2: API Keys

Displays the project's API keys with copy buttons and visibility toggles:

- **Public key** (safe for client-side): `eb_pk_...` — Explanation: 'Use this in your frontend. It can only read public data and authenticate users.'
- **Secret key** (server-side only): `eb_sk_...` — Explanation: 'Use this in your server/backend. It has full access. Never expose this in client code.' Shown with a warning icon.
- **Project URL:** `https://{slug}.eurobase.eu` — Explanation: 'This is your API endpoint. All SDK calls go here.'

#### Tab 3: Next Steps (with checkboxes)

- Create your first table (links to `/p/{id}/database` with a 'Create Table' dialog pre-opened)
- Upload a file (links to `/p/{id}/storage` with the upload dialog pre-opened)
- Add auth to your app (links to a quickstart guide page)
- Invite a team member (links to `/account` with the team section focused)

**CTA button:** "Go to Dashboard" (navigates to `/p/{id}`)

---

## 8. Data Models and Database Schema

Eurobase has two levels of database schema: the platform schema (public) that manages projects and platform users, and the per-tenant schemas that hold customer data.

### 8.1 Platform Schema (public)

#### `public.platform_users`

| Column | Type | Description |
|--------|------|-------------|
| `id` | UUID PK | Platform user ID |
| `hanko_user_id` | TEXT UNIQUE | Hanko subject ID from platform Hanko project |
| `email` | TEXT | Email address |
| `display_name` | TEXT | Display name |
| `mollie_customer_id` | TEXT | Mollie customer ID for billing |
| `plan` | TEXT DEFAULT 'free' | Account-level plan |
| `created_at` | TIMESTAMPTZ | Registration timestamp |

#### `public.projects`

| Column | Type | Description |
|--------|------|-------------|
| `id` | UUID PK | Project ID (used as tenant_id throughout) |
| `owner_id` | UUID FK → platform_users | Creator/owner of the project |
| `name` | TEXT | Display name ('My Awesome App') |
| `slug` | TEXT UNIQUE | URL slug, used for subdomain and schema name |
| `schema_name` | TEXT UNIQUE | PostgreSQL schema name (tenant_{slug}) |
| `s3_bucket` | TEXT UNIQUE | S3 bucket name (eb-{slug}) |
| `region` | TEXT DEFAULT 'fr-par' | Scaleway region |
| `plan` | TEXT DEFAULT 'free' | Project plan tier |
| `status` | TEXT DEFAULT 'provisioning' | provisioning \| active \| suspended \| deleting |
| `auth_config` | JSONB | Hanko configuration (methods, redirect URLs, etc.) |
| `settings` | JSONB DEFAULT '{}' | Project-level settings (CORS, rate limits, etc.) |
| `created_at` | TIMESTAMPTZ | Creation timestamp |

#### `public.api_keys`

| Column | Type | Description |
|--------|------|-------------|
| `id` | UUID PK | Key ID |
| `project_id` | UUID FK → projects | Which project this key belongs to |
| `key_hash` | TEXT | SHA-256 hash of the API key (never store plaintext) |
| `key_prefix` | TEXT | First 8 chars for identification (eb_pk_xxxx...) |
| `type` | TEXT | 'public' (read-only) or 'secret' (full access) |
| `created_at` | TIMESTAMPTZ | Creation timestamp |
| `last_used_at` | TIMESTAMPTZ | Last request timestamp (updated async) |

#### `public.subscriptions`

| Column | Type | Description |
|--------|------|-------------|
| `id` | UUID PK | Subscription ID |
| `project_id` | UUID FK → projects | Which project this subscription covers |
| `mollie_subscription_id` | TEXT UNIQUE | Mollie subscription reference |
| `plan` | TEXT | Plan name (pro, enterprise) |
| `status` | TEXT | active \| cancelled \| overdue |
| `current_period_start` | TIMESTAMPTZ | Current billing period start |
| `current_period_end` | TIMESTAMPTZ | Current billing period end |
| `created_at` | TIMESTAMPTZ | Creation timestamp |

#### `public.invoices`

| Column | Type | Description |
|--------|------|-------------|
| `id` | UUID PK | Invoice ID |
| `project_id` | UUID FK → projects | Which project was charged |
| `mollie_payment_id` | TEXT UNIQUE | Mollie payment reference |
| `amount_cents` | INTEGER | Amount in euro cents |
| `currency` | TEXT DEFAULT 'EUR' | Currency code |
| `status` | TEXT | paid \| failed \| refunded |
| `paid_at` | TIMESTAMPTZ | Payment timestamp |
| `created_at` | TIMESTAMPTZ | Creation timestamp |

#### `public.webhooks`, `public.webhook_deliveries`

Webhook configuration and delivery logs follow the same pattern. Webhook config references a `project_id`, stores URL, events array, signing secret hash, and enabled boolean. Deliveries reference the webhook and store request/response data, status code, and attempt count.

### 8.2 Per-Tenant Schema (`tenant_{slug}`)

The per-tenant schema is created by the provisioning orchestrator. The default tables (`users`, `collections`, `documents`, `storage_objects`) are described in the previously delivered SQL file. In addition to these defaults, tenants can create custom tables via the schema editor. Custom tables are created in the same schema and automatically get RLS policies applied.

---

## 9. API Contract — Endpoint Reference

The API is served at `https://{project-slug}.eurobase.eu/v1/`. The `/v1/` prefix allows future API version evolution without breaking existing clients.

### 9.1 Platform Management Endpoints (used by the console)

| Method | Path | Description |
|--------|------|-------------|
| **POST** | `/platform/projects` | Create a new project (triggers provisioning) |
| **GET** | `/platform/projects` | List all projects for the authenticated user |
| **GET** | `/platform/projects/{id}` | Get project details + status |
| **PATCH** | `/platform/projects/{id}` | Update project settings |
| **DELETE** | `/platform/projects/{id}` | Delete project (triggers deprovision) |
| **POST** | `/platform/projects/{id}/api-keys` | Generate new API key |
| **GET** | `/platform/projects/{id}/schema` | Get tenant schema (tables, columns, types) |
| **POST** | `/platform/projects/{id}/schema/tables` | Create a new table (DDL) |
| **PATCH** | `/platform/projects/{id}/schema/tables/{t}` | Alter table (add/drop/rename column) |
| **GET** | `/platform/projects/{id}/usage` | Get usage metrics (rows, storage, requests) |
| **POST** | `/platform/webhooks/mollie/payment` | Mollie payment webhook handler |
| **POST** | `/platform/webhooks/mollie/subscription` | Mollie subscription webhook handler |

### 9.2 Data API Endpoints (used by client SDKs)

| Method | Path | Description |
|--------|------|-------------|
| **GET** | `/v1/db/{table}` | Query rows with filters, select, order, pagination |
| **POST** | `/v1/db/{table}` | Insert row(s) |
| **PATCH** | `/v1/db/{table}/{id}` | Update row by ID |
| **DELETE** | `/v1/db/{table}/{id}` | Delete row by ID |
| **POST** | `/v1/db/rpc/{fn}` | Call stored function |
| **POST** | `/v1/storage/upload` | Upload file |
| **GET** | `/v1/storage/{key}` | Download file |
| **DELETE** | `/v1/storage/{key}` | Delete file |
| **GET** | `/v1/storage` | List files |
| **POST** | `/v1/storage/signed-url` | Generate pre-signed URL |
| **WS** | `/v1/realtime` | WebSocket connection for live updates |

---

## 10. Extension Architecture — Future-Proofing

The MVP ships with 3 services (auth, database, storage) but the architecture must accommodate future services without refactoring the core. This is achieved through a provider interface pattern in the Go codebase.

### 10.1 Provider Interface

Each service type implements a common provider interface:

```go
type ServiceProvider interface {
    Name() string
    Provision(ctx context.Context, project *Project) error
    Deprovision(ctx context.Context, project *Project) error
    HealthCheck(ctx context.Context, project *Project) (Status, error)
    Routes() []Route  // HTTP routes this provider adds to the gateway
}
```

The MVP has three providers: `AuthProvider` (Hanko), `DatabaseProvider` (Scaleway PG), and `StorageProvider` (Scaleway S3). Each is registered in a provider registry at gateway startup. The provisioning orchestrator iterates over all registered providers when creating or deleting a project.

### 10.2 Planned Future Providers

| Service | Target Version | Provider Implementation |
|---------|---------------|------------------------|
| Edge Functions | v1.2 | Scaleway Serverless Functions or self-hosted Deno Deploy. Adds `/v1/functions/{name}` route. |
| Push Notifications | v1.3 | EU push service (or self-hosted via FCM/APNs proxy). Adds `/v1/push` endpoint. |
| Vector Search / AI | v1.3 | pgvector extension on Scaleway PG. Adds `/v1/db/vector/search` endpoint. |
| Cron Jobs | v1.2 | River scheduled jobs. Adds `/v1/cron` CRUD and console UI. |
| GraphQL | v1.1 | Auto-generated GraphQL schema from tenant tables. Adds `/v1/graphql` endpoint. |
| Email / SMS | v1.2 | Scaleway TEM for email, EU SMS provider. Adds `/v1/messaging` endpoint. |

### 10.3 Console Extension Pattern

The console sidebar uses a service registry to render navigation items. Each service has a manifest (name, icon, route prefix, enabled flag). For the MVP, auth/database/storage are always enabled. Future services can be toggled per project, and the sidebar and routes adjust dynamically. This is implemented as a Svelte store that reads the project's `enabled_services` array from the API.

---

## 11. Development Phases and Sprint Plan

The MVP is estimated at 10–12 weeks of development for a team of 2–3 full-time engineers (1 backend, 1 frontend, 1 full-stack). The work is divided into 4 phases.

### Phase 1: Foundation (Weeks 1–3)

**Goal:** All infrastructure provisioned, core gateway running, basic authentication working.

- Set up Scaleway infrastructure via Terraform: Kapsule cluster, managed PostgreSQL, Redis, Object Storage, Container Registry, DNS.
- Implement gateway skeleton: Go project structure, chi router, health endpoint, Dockerfile, Kubernetes deployment.
- Implement Hanko JWT validation middleware. Set up platform Hanko project for console auth.
- Implement tenant context middleware: `set_tenant_context()` integration, connection pool management.
- Implement platform management endpoints: create/list/get/delete projects.
- Implement provisioning orchestrator: `provision_tenant()` + S3 bucket creation + API key generation.
- Set up CI/CD pipeline: GitHub Actions → build → test → push to Scaleway CR → deploy to Kapsule.

**Deliverable:** A gateway that can create a project, provision a schema + bucket, and validate JWTs. Testable via curl.

### Phase 2: Data Layer (Weeks 4–6)

**Goal:** Full CRUD API for database and storage, queryable via REST.

- Implement query engine: SELECT with filters, ordering, pagination, column selection.
- Implement INSERT, UPDATE, DELETE handlers with input validation.
- Implement schema introspection endpoint (reads `pg_catalog` for the tenant schema).
- Implement schema DDL endpoints (CREATE TABLE, ALTER TABLE) with RLS auto-application.
- Implement storage proxy: upload, download, delete, list, pre-signed URLs.
- Implement rate limiter middleware (Redis token bucket).
- Implement realtime engine: WebSocket upgrade, Redis pub/sub bridge, change event publishing.
- Write the TypeScript SDK: `createClient`, `db.from().select()`, `storage.upload()`, `realtime.on()`.
- Publish SDK to npm as `@eurobase/sdk`.

**Deliverable:** A working BaaS API. A developer can create a project, insert data, query it, upload files, and receive realtime events — all via the SDK.

### Phase 3: Console (Weeks 7–9)

**Goal:** Full console UI with onboarding flow.

- Set up SvelteKit project with TypeScript, Tailwind CSS, and component library (consider shadcn-svelte).
- Implement console authentication: Hanko Elements login/signup pages.
- Implement onboarding wizard (3 steps as designed in Section 7).
- Implement project list page and project overview dashboard.
- Implement database page: table list, table data grid, schema editor, SQL runner.
- Implement storage browser: file upload, folder navigation, preview, delete.
- Implement authentication settings page (Hanko config UI).
- Implement API Explorer: auto-generated from schema, try-it-now panel.
- Implement project settings page, API key management, webhooks UI.
- Implement account/billing page: Mollie integration for plan selection, SEPA mandate creation, payment method management, invoice history.

**Deliverable:** A fully functional web console. A developer can sign up, complete onboarding, manage their database and storage, and view API docs — all from the browser.

### Phase 4: Polish and Launch Prep (Weeks 10–12)

**Goal:** Production-ready quality, documentation, beta launch.

- End-to-end testing: signup → onboarding → create table → insert data → query via SDK → upload file → realtime event.
- Load testing: simulate 100 concurrent tenants with realistic query patterns. Identify bottlenecks.
- Security audit: RLS policy review, SQL injection testing, JWT validation edge cases, S3 bucket policy review.
- Error handling and edge cases: graceful degradation, clear error messages, retry logic.
- Documentation: quickstart guide, SDK reference, API reference, self-hosting guide.
- Landing page: marketing site at eurobase.eu with value proposition, pricing, and sign-up CTA.
- Monitoring setup: Grafana dashboards for request rates, error rates, latency percentiles, DB connections, storage usage.
- Beta launch: invite 10–20 developers from the legaltech target list for closed beta.

**Deliverable:** Production-ready MVP. Closed beta with real users.

---

## 12. Testing Strategy

### Unit Tests (Go)

Each package in `pkg/` has unit tests covering core logic: query building, SQL parameter sanitization, tenant ID validation, JWT parsing, rate limit calculation. Target: 80% coverage on `pkg/`. Use Go's standard `testing` package + testify for assertions.

### Integration Tests (Go)

Use `testcontainers-go` to spin up PostgreSQL and Redis containers for integration testing. Tests cover: tenant provisioning end-to-end, query engine against real PostgreSQL (including RLS verification), storage operations against a MinIO container (S3-compatible), and WebSocket realtime events. Target: every API endpoint has at least one integration test.

### E2E Tests (Playwright)

The console is tested with Playwright: sign up flow, onboarding wizard completion, table creation, data insertion, file upload, and API key display. Run against a staging environment that mirrors production.

### Security Tests

Dedicated test suite for tenant isolation: create two tenants, insert data into each, verify that tenant A cannot read/write tenant B's data under any condition (wrong search_path, direct SQL, storage key guessing). This test suite runs on every CI build.

---

## 13. Deployment and CI/CD

### 13.1 Container Images

Two images: `eurobase-gateway` (Go, ~20MB) and `eurobase-console` (SvelteKit, Node adapter or static). Both built via multi-stage Dockerfiles and pushed to Scaleway Container Registry.

### 13.2 Kubernetes Resources

- **gateway Deployment:** 2 replicas minimum, HPA (Horizontal Pod Autoscaler) targeting 70% CPU, max 10 replicas. Readiness probe: `GET /health`. Resource requests: 256Mi RAM, 250m CPU.
- **worker Deployment:** 1 replica (scales manually for now). Processes River jobs. Same image as gateway with a different entrypoint flag (`--mode=worker`).
- **console Deployment:** 2 replicas. Serves the SvelteKit app. Alternatively, deploy as static site to Scaleway Object Storage + Edge Services.
- **Ingress:** Nginx ingress controller with TLS via cert-manager (Let's Encrypt). Routes: `*.eurobase.eu` → gateway, `console.eurobase.eu` → console.
- **Secrets:** managed via Scaleway Secret Manager, injected into pods as environment variables.

### 13.3 CI/CD Pipeline (GitHub Actions)

```
on push to main:
  1. Run Go tests (unit + integration via testcontainers)
  2. Run Svelte build + Playwright tests
  3. Build Docker images
  4. Push to Scaleway Container Registry
  5. kubectl rollout restart deployment/gateway
  6. kubectl rollout restart deployment/console
  7. Verify health endpoints return 200
```

---

## 14. Open Questions and Decisions

### Open Questions

| # | Question | Context / Options |
|---|----------|-------------------|
| 1 | **Hanko multi-tenancy model** | Does each Eurobase project get its own Hanko project, or do we use a single Hanko project with tenant-scoped users? Affects provisioning and billing. Need to clarify with Hanko team. |
| 2 | **Custom domains timeline** | Customers will want myapp.com instead of slug.eurobase.eu. Requires wildcard TLS, DNS verification, and ingress routing. Deferred to v1.1 but architecture should not block it. |
| 3 | **Console hosting: SSR vs static** | SvelteKit can deploy as SSR (needs Node runtime on Kapsule) or static (deploy to S3 + CDN). SSR enables server-side auth checks but adds infra. Static is simpler. Recommendation: static for MVP, SSR for v1.1. |
| 4 | **GraphQL: build vs adopt PostGraphile** | PostGraphile auto-generates GraphQL from PostgreSQL schemas. Could save weeks of development for v1.1. Trade-off: it's a Node.js dependency in a Go stack. Alternative: build a Go GraphQL layer using gqlgen. |
| 5 | **Row-level permissions for end-users** | The current RLS isolates tenants. But customers also need to define permissions for their end-users (e.g., 'users can only read their own rows'). This needs a permission rule language. Supabase uses PostgreSQL RLS policies. Do we expose raw RLS or build an abstraction layer? |
| 6 | **SDK name and npm scope** | @eurobase/sdk, @eurobase/js, eurobase, or eb? The npm scope @eurobase must be reserved. Also need to decide the package name for future SDKs (@eurobase/dart, etc.). |

### Resolved Decisions

| # | Decision | Resolution |
|---|----------|------------|
| ✓ | **Billing provider** | **RESOLVED: Mollie (Amsterdam, NL).** EU-incorporated, SEPA direct debit support, no US data transfer, competitive pricing. Maintains Eurobase's 100% EU data sovereignty across the entire stack. Integration via Mollie REST API v2 with Go client. See Section 6.4 for full implementation details. |

---

*End of Document — Eurobase MVP Implementation Plan v1.1 — March 2026 — Updated: Mollie billing integration*
