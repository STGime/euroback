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
- `eurobase_ddl_runner` — runtime role used by the gateway pod for **tenant schema migrations** (`POST /platform/projects/{id}/migrations`, #190). Same shape as `eurobase_function_runner` but for DDL: created `WITH LOGIN NOINHERIT`, NO direct grants on `public.*` (beyond USAGE + helper EXECUTE + SELECT/INSERT on `public.tenant_migrations`), member of every per-tenant `<schema>_ddl` role. The gateway runs each migration with `SET LOCAL ROLE <schema>_ddl`; because the login role is privilege-less and NOINHERIT, a malicious migration body that does `RESET ROLE` lands harmlessly here (cannot reach `public.*` or another tenant). Wired via `DATABASE_URL_DDL_RUNNER` in the same Secret; if unset, the migrations endpoint fails closed (503). Per-tenant `<schema>_ddl` roles (NOLOGIN, CREATE on own schema only, own the tenant's application tables) are created by `provision_tenant` (migration `000063`).
- `eurobase_api` — legacy admin role kept for rollback. Once the cutover is proven, delete it via the Scaleway console.
- All runtime login roles must be created via the Scaleway console **before** their migrations run (`000037` gateway/migrator, `000044` developer, `000047` function_runner, `000063` ddl_runner — `CREATE ROLE eurobase_ddl_runner WITH LOGIN NOINHERIT`). The migration files only do GRANT / REVOKE / membership.
- Never issue `DATABASE_URL` (or `DATABASE_URL_DEVELOPER` / `DATABASE_URL_FUNCTION_RUNNER` / `DATABASE_URL_DDL_RUNNER`) to tenants. The gateway exposes data via SDK + REST only.

## Functions runner HMAC
- Gateway → runner traffic is HMAC-SHA256-signed using `FUNCTIONS_RUNNER_HMAC_SECRET` (≥32 bytes) shared via the `eurobase-secrets` k8s Secret. Both gateway and functions Deployments read it via `envFrom: secretRef: eurobase-secrets`.
- Generate via `openssl rand -hex 32`. Rotate by setting a new value and rolling both Deployments together.
- Runner enforcement is controlled by `FUNCTIONS_RUNNER_HMAC_REQUIRE_SIGNED`:
  - `true` → strict; missing or invalid signature → 401.
  - unset/other → soft mode (warn-only on missing); invalid signature still 401. Use during rollout window.
- Gateway aborts startup if the secret is missing in production (`ENV=production` or `DOMAIN_SUFFIX` ends with `eurobase.app`).

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
