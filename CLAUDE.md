# Eurobase Project: 

## API
- Base URL: https://newtek2-njyf.eurobase.app
- Project ID: 16a1a87c-6c7e-43b7-a9d5-03e6a46e9555

## Database
- Connection: see .env for DATABASE_URL
- All tenant-scoped queries must use RLS with set_tenant_id()
- System tables (users, refresh_tokens, storage_objects, email_tokens, vault_secrets) are managed by the platform

## Postgres roles
- `eurobase_gateway` — runtime role used by the gateway + worker pods for **SDK runtime traffic** (`/v1/*`). DML only on `public.*`, USAGE + CREATE on tenant schemas so the SDK DDL endpoint works. NO DDL on `public.*`. Wired via `DATABASE_URL` in the `eurobase-secrets` k8s Secret.
- `eurobase_developer` — runtime role used by the gateway pod for **platform-authenticated developer traffic** (console + MCP under `/platform/*`). Member of `eurobase_migrator` with INHERIT, so it gets ownership-equivalent privileges. Each platform tx runs `SET LOCAL ROLE eurobase_migrator`, so DDL/REFERENCES against migrator-owned tables works and any newly created objects are owned by the migrator (uniform with CI-applied migrations). Wired via `DATABASE_URL_DEVELOPER` in the same Secret. **Two distinct DB pools share one process by design** — runtime exploit ≠ elevated privileges.
- `eurobase_migrator` — deploy-only role. Owns `public.*` tables and tenant schemas; runs migrations via the `migrate` Kubernetes Job in CI. Wired via `DATABASE_URL_MIGRATOR` in the same Secret.
- `eurobase_api` — legacy admin role kept for rollback. Once the cutover is proven, delete it via the Scaleway console.
- All three runtime roles must be created via the Scaleway console **before** their migrations run (`000037` for gateway/migrator, `000043` for developer). The migration files only do GRANT / REVOKE / membership.
- Never issue `DATABASE_URL` (or `DATABASE_URL_DEVELOPER`) to tenants. The gateway exposes data via SDK + REST only.

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
