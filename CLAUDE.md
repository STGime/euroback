Eurobase
EU-sovereign Backend-as-a-Service platform. Zero US CLOUD Act exposure.
A GDPR-native alternative to Supabase/Firebase for developers building
B2B products for European customers in compliance-sensitive verticals.
Agents
AgentOwnsbackend-agentGo gateway, River workers, Scaleway integrationsfrontend-agentSvelteKit console, Tailwind UI, Hanko auth flowdb-agentPostgreSQL schema, migrations, RLS policies
When a task spans multiple agents, delegate to each in dependency order:
db-agent first (schema) → backend-agent (API) → frontend-agent (UI).
Full stack
ConcernProvider / TechnologyJurisdictionAuthHanko GmbHDEDatabaseScaleway managed PostgreSQLFRObject storageScaleway Object StorageFRKubernetesScaleway KapsuleFRRedisScaleway RedisFREmailScaleway TEMFRObservabilityScaleway CockpitFRBillingMollieNLAPI languageGo—ConsoleSvelteKit + Tailwind CSS v4—Job queueRiver (PostgreSQL-backed)—
Sovereignty constraint (hard rule)
No US-incorporated services. No AWS, GCP, Azure, Cloudflare, Stripe,
Vercel, or any entity subject to the US CLOUD Act. This is the core
product promise and cannot be compromised for convenience.
Repository layout
/cmd/
  gateway/        — Go gateway entrypoint
  worker/         — River worker entrypoint
/internal/
  gateway/        — HTTP handlers, middleware, routing
  workers/        — River job definitions
  billing/        — Mollie integration
  storage/        — Scaleway S3 integration
  email/          — Scaleway TEM integration
  cache/          — Redis wrappers
  auth/           — Hanko JWT validation
  tenant/         — Tenant context propagation
  db/             — DB client, query helpers
/migrations/      — golang-migrate SQL files
/console/         — SvelteKit application
/docs/
  api/            — API contract specs (kept in sync with implementation)
  mvp-plan-v1.1.md
  architecture/
Current MVP phase
Phase 1 — Core Infrastructure (Weeks 1–3)
Goals: tenant provisioning, Hanko auth webhook, Scaleway bucket init per project,
basic project CRUD, console auth flow and dashboard skeleton.
See /docs/mvp-plan-v1.2.md for full phase breakdown.
Non-negotiables

RLS + set_tenant_id() on every tenant-scoped DB operation — no exceptions
Mollie only for billing — Stripe is explicitly excluded
Structured logging (slog) throughout Go code — no fmt.Println
All credentials from env vars — nothing hardcoded
EU member states only for all infrastructure — UK and Switzerland excluded
System tables (users, refresh_tokens, storage_objects, email_tokens, vault_secrets) must be hidden from all user-facing UI (Table Editor, SQL Editor, API Explorer, Schema Diagram, Connect page, Cron schema browser, overview table count)