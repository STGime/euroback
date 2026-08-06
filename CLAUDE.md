# Eurobase Project: 

## API
- Base URL: https://newtek2.eurobase.app
- Project ID: b24e9fa8-463f-452d-be4e-ee5127c3e8f7

## Database
- Connection: see .env for DATABASE_URL
- All tenant-scoped queries must use RLS with set_tenant_id()
- System tables (users, refresh_tokens, storage_objects, email_tokens, vault_secrets) are managed by the platform

## Postgres roles
- `eurobase_gateway` — runtime role used by the gateway + worker pods for **SDK runtime traffic** (`/v1/*`). DML only on `public.*`, USAGE + CREATE on tenant schemas so the SDK DDL endpoint works. NO DDL on `public.*`. Wired via `DATABASE_URL` in the `eurobase-secrets` k8s Secret.
- `eurobase_developer` — runtime role used by the gateway pod for **platform-authenticated developer traffic** (console + MCP under `/platform/*`). Member of `eurobase_migrator` with INHERIT, so it gets ownership-equivalent privileges. Each platform tx runs `SET LOCAL ROLE eurobase_migrator`, so DDL/REFERENCES against migrator-owned tables works and any newly created objects are owned by the migrator (uniform with CI-applied migrations). Wired via `DATABASE_URL_DEVELOPER` in the same Secret. **Two distinct DB pools share one process by design** — runtime exploit ≠ elevated privileges.
- `eurobase_migrator` — deploy-only role. Owns `public.*` tables and tenant schemas; runs migrations via the `migrate` Kubernetes Job in CI. Wired via `DATABASE_URL_MIGRATOR` in the same Secret.
- `eurobase_function_runner` — runtime role used by the **edge functions runner pod** (deploy/k8s/functions.yaml). NO direct grants on any tenant schema or `public.*` (beyond USAGE + helper-function EXECUTE). Member of every per-tenant `<schema>_func` role; the runner does `SET LOCAL ROLE <schema>_func` per invocation so user JS can only reach the executing tenant. Wired via `DATABASE_URL_FUNCTION_RUNNER` in the same Secret. Per-tenant `<schema>_func` roles are created by `provision_tenant` (migration `000047`).
- `<schema>_ddl` — **per-tenant** runtime role for **tenant schema migrations** (`POST /platform/projects/{id}/migrations`, #190). NOLOGIN until use; CREATE on its own schema only, USAGE + helper EXECUTE on `public`, NO `public.*` table grants, **member of nothing**. Owns the tenant's application tables. Created by `provision_tenant` (migration `000063`). To run a migration, the gateway (via the developer pool, as migrator) sets the role's LOGIN password (`ALTER ROLE … PASSWORD`, derived `HMAC-SHA256(DDL_PASSWORD_SECRET, schema)`), then opens a short-lived connection **as that role** and runs the SQL. Because the connecting role is a member of exactly one tenant, a malicious migration body that does `RESET ROLE` lands back on the same role and **cannot pivot into another tenant** (a shared login role member of all tenants would be — that was the #209 review finding). Bookkeeping is via two `session_user`-bound SECURITY DEFINER helpers (`record_tenant_migration` / `tenant_migration_checksum`), so a tenant role has no direct grant on `public.tenant_migrations` and can neither forge nor read other projects' history. Requires `DDL_PASSWORD_SECRET` (≥32 bytes) in `eurobase-secrets`; if unset, the endpoint fails closed (503). **Two Scaleway-owned-object grants `eurobase_migrator` needs** (both because the `eurobase` DB and schema `public` are owned by `_rdb_superadmin` / `pg_database_owner`, not the customer — a grant attempted as a non-owner is a silent WARNING-not-error no-op): **(1) DB CONNECT** — `eurobase_migrator` must own the `eurobase` database or hold `CONNECT … WITH GRANT OPTION`, so it can grant CONNECT to the per-tenant roles; **(2) schema-public USAGE** — `eurobase_migrator` must hold `USAGE ON SCHEMA public … WITH GRANT OPTION`, so it can grant USAGE to the `_ddl` roles. The `_ddl` role needs USAGE on `public` because the apply path makes *ad-hoc, parse-time* `public.*` references (the `tenant_migration_checksum` / `record_tenant_migration` bookkeeping calls, and `CREATE POLICY … public.is_service_role()` in migration bodies) — EXECUTE on those functions is not enough, schema USAGE is checked at name-resolution time. Both grants are obtained once from Scaleway support (as `_rdb_superadmin`); the apply path then re-grants **and verifies** each per-tenant role's CONNECT (`has_database_privilege`) and public USAGE (`has_schema_privilege`) on every `migrations up`, failing loud if either can't be granted. (Note: func roles do **not** need public USAGE — RLS policies are OID-resolved, so policy evaluation checks only EXECUTE, which `provision_tenant` grants.) Reproducible isolation proof: `scripts/verify-tenant-migration-isolation.sh`.
- **Lockdown (migration `000064`, #217):** `provision_tenant_ddl_role(text)` was created with a NULL ACL (default `EXECUTE → PUBLIC`); once a per-tenant role has USAGE on `public` that becomes a cross-tenant footgun (a migration body / edge-function SQL could call this migrator-owned SECURITY DEFINER function), so `000064` does `REVOKE EXECUTE … FROM PUBLIC` on it (it's only ever called in migrator context, so no re-grant is needed). A #217 sweep confirmed it was the only PUBLIC-executable privileged secdef function in `public`. **CONVENTION — recurrence prevention:** every **new** `SECURITY DEFINER` / helper function created by migrator in schema `public` MUST `REVOKE EXECUTE … FROM PUBLIC` (or `GRANT EXECUTE` only to the roles that need it — `eurobase_gateway` / `eurobase_function_runner` / per-tenant roles) **in its own migration**. The obvious blanket guard (`ALTER DEFAULT PRIVILEGES FOR ROLE eurobase_migrator IN SCHEMA public REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC`) was tested against PG 16 and is a **no-op** (it does not suppress PUBLIC EXECUTE for a non-bootstrap owner role), so do not rely on it — the per-function REVOKE is the rule. `scripts/verify-tenant-migration-isolation.sh` check 11 asserts the lock holds.
- `eurobase_api` — legacy admin role kept for rollback. Once the cutover is proven, delete it via the Scaleway console.
- The shared runtime login roles must be created via the Scaleway console **before** their migrations run (`000037` gateway/migrator, `000044` developer, `000047` function_runner). The migration files only do GRANT / REVOKE / membership. (Per-tenant `<schema>_func` and `<schema>_ddl` roles are NOLOGIN and created by `provision_tenant`, not the console.)
- Never issue `DATABASE_URL` (or `DATABASE_URL_DEVELOPER` / `DATABASE_URL_FUNCTION_RUNNER`) to tenants. The gateway exposes data via SDK + REST only.

## Functions runner HMAC
- Gateway → runner traffic is HMAC-SHA256-signed using `FUNCTIONS_RUNNER_HMAC_SECRET` (≥32 bytes) shared via the `eurobase-secrets` k8s Secret. Both gateway and functions Deployments read it via `envFrom: secretRef: eurobase-secrets`.
- Generate via `openssl rand -hex 32`. Rotate by setting a new value and rolling both Deployments together.
- Runner enforcement is controlled by `FUNCTIONS_RUNNER_HMAC_REQUIRE_SIGNED`:
  - `true` → strict; missing or invalid signature → 401.
  - unset/other → soft mode (warn-only on missing); invalid signature still 401. Use during rollout window.
- Gateway aborts startup if the secret is missing in production (`ENV=production` or `DOMAIN_SUFFIX` ends with `eurobase.app`).

## Team-tier runtime password (M2.5 part 2b)
- `RUNTIME_PASSWORD_SECRET` (≥32 bytes) in `eurobase-secrets` derives the deterministic `eurobase_gateway` login password on each dedicated managed-PG instance: `HMAC-SHA256(secret, project_database_id)`, hex64. Same pattern as `DDL_PASSWORD_SECRET`. Deterministic derivation is what lets concurrent runners (provision retry + backfill sweeper) set the same live Scaleway password so the persisted ciphertext in `project_databases.runtime_*` can't diverge from what Scaleway holds. Generate via `openssl rand -hex 32`.
- Empty is legal (dev) — worker skips the bootstrap step and leaves `runtime_username` NULL; SDK routing falls back to shared cluster. Too-short (< 32 bytes) fails worker startup rather than silently deriving from a weak HMAC key.

## Mollie billing
- Payment processor is Mollie (Dutch, EU-headquartered — already in `sub_processors` from migration `000025`).
- API-key secrets in `eurobase-secrets`: `MOLLIE_API_KEY_TEST` (starts `test_`), `MOLLIE_API_KEY_LIVE` (starts `live_`), `MOLLIE_ENV` (`test`|`live`). The client's `NewClient` construction logs a warning if the API-key prefix doesn't match the env, so ops key mixups are visible in the first minute after deploy.
- **Test key first, live key on the flip.** Staging always runs `MOLLIE_ENV=test`; prod runs `MOLLIE_ENV=test` until PR 8 lands and `BILLING_ENABLED=true`, then flips to `live`.
- Empty API key is legal — client construction succeeds and every endpoint returns `ErrUnauthorized` without hitting the network. Lets dev environments boot without secrets.
- Feature flag: `BILLING_ENABLED` (default false in prod, true in staging). All billing HTTP handlers return 503 when false (not 404 — the route is registered so 503 is a clearer signal of "temporarily off" than "doesn't exist").
- URL config: `CONSOLE_BASE_URL` (post-checkout redirect target, default `https://console.eurobase.app`), `PLATFORM_BASE_URL` (webhook URL Mollie POSTs to, default `https://api.eurobase.app`).
- **Estonian VAT status:** Eurobase OÜ is below the €40k VAT threshold, so invoices carry "Not VAT-registered under Estonian VAT Act §19" and no VAT is charged. Register for VAT and switch to reverse-charge B2B / local-VAT B2C on crossing the threshold.
- **Recurring flow.** Mollie doesn't create a subscription until a mandate exists. Checkout (PR 3) creates a first Payment with `sequenceType=first`; the user completes it on Mollie's page; the webhook (PR 4) sees `paid` + `mandateId`, then creates the recurring Subscription against the mandate.

## Auth
- Custom auth built in Go (email/password, magic links, OAuth)
- Anon key for public client access
- Service key for server-side access only — never expose in client code

## Build & Deploy
- Backend builds and deploys via GitHub Actions (push to main triggers CI/CD)
- Do not run deploy scripts manually — just commit and push

## Compliance / Sub-Processors
When adding a new third-party data processor, three things must be updated:
1. Insert the processor into the `sub_processors` DB table (via migration)
2. Add feature detection in `internal/compliance/registry.go` → `resolveActiveFeatures()`
3. Link the processor to the feature in the `service_dependencies` table (via same migration)

This ensures the compliance DPA report automatically includes the processor when the feature is enabled.

## Sovereignty
- All infrastructure runs in EU (France) on Scaleway
- No US cloud services permitted (AWS, GCP, Azure, Cloudflare, Stripe, Vercel)
