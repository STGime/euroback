# Eurobase Project

@~/.claude/euroback-private-notes.md

> **This repository is public.** The import above loads maintainers' private
> operational and security notes (secret-derivation details, production
> state, in-flight security work) on maintainers' machines. **Keep this file
> to architecture and conventions.** Never write into this file — or into
> issues, PRs, commit messages or code comments — descriptions of open
> vulnerabilities, ways around a guard, production secrets or their
> derivation, live production state (ACLs, pending provider tickets, leftover
> grants), or anything that reads as an attack map. Put those in the private
> notes.

## API
- Base URL: https://newtek.eurobase.app
- Project ID: 93df6379-8b4c-4056-8e61-647bffd9aa7c

## Database
- Connection: see `.env` for `DATABASE_URL`.
- All tenant-scoped queries run under RLS.
- System tables in a tenant schema (`users`, `user_identities`, `refresh_tokens`, `email_tokens`, `storage_objects`, `storage_shared_prefixes`, `vault_secrets`) are platform-managed — owned by the migrator, with their own console pages / APIs. Don't treat them as application tables.

## Postgres roles
Four shared platform login roles plus per-tenant roles. Each shared role has its own `DATABASE_URL*` in the `eurobase-secrets` k8s Secret.
- `eurobase_gateway` — SDK runtime traffic (`/v1/*`). DML on `public.*`; no DDL on `public.*`. Deliberately a **non-owner** of tenant tables so RLS is enforced for SDK traffic.
- `eurobase_developer` — platform-authenticated traffic (console + MCP under `/platform/*`). Runs platform transactions as `eurobase_migrator` (via `SET LOCAL ROLE`) for DDL and ownership-equivalent access. **Two distinct pools share one process by design.**
- `eurobase_migrator` — deploy-only; owns `public.*` and tenant schemas; runs migrations via the `migrate` k8s Job in CI. An INHERIT member of each `<schema>_ddl` (set in `provision_tenant_ddl_role`) and of `eurobase_gateway` — **do not revoke these memberships**: the platform data API relies on them for owner-equivalent access to `_ddl`- and gateway-owned tenant tables (custom-RLS tables that lack a service-role branch depend on this bypass).
- `eurobase_function_runner` — the edge-functions runner's own login. Loads function code and vault secrets only through dedicated helper functions; **customer SQL never runs on it**.
- `<schema>_func` / `<schema>_ddl` — **per-tenant** roles created by `provision_tenant`, member of nothing. `_func` runs customer SQL (edge functions, cron); `_ddl` owns the tenant's application tables and runs tenant migrations. Each connects with its own derived password.

**Conventions (these guide code changes):**
- **Customer SQL runs on a per-project login.** Any SQL a customer supplies or defines (SDK SQL, data API, SQL editor, MCP SQL, migrations, functions, cron, routine bodies) must execute on a connection that logs in as that project's own role — never on a shared platform login. (Edge functions and cron already do; the SDK/editor/data-API paths are being moved — see the private notes / Notion plan.)
- **Gateway pool can't read `organizations` / `org_members` / `support_requests` / `contact_requests`** — SELECT is revoked from `eurobase_gateway`. Any query joining these runs on the developer pool. Failure signature if you get it wrong: prod 500 with `SQLSTATE 42501 permission denied`. Fix: split so the gateway-pool side touches only `platform_users` and the developer pool answers membership questions.
- **Every new `SECURITY DEFINER` / helper function the migrator creates in `public` must `REVOKE EXECUTE … FROM PUBLIC`** (or grant EXECUTE only to the roles that need it) in its own migration. The blanket `ALTER DEFAULT PRIVILEGES … REVOKE EXECUTE` guard is a no-op for a non-bootstrap owner — the per-function REVOKE is the rule.
- **No `pg_`-prefixed identifiers in customer SQL** (SDK and platform SQL paths): `query.ValidateNoCatalogRefs` refuses them, with a tiny allowlist. See the private notes for the full rationale and the DB-level controls.
- **Never issue `DATABASE_URL*` to tenants.** Data is exposed via SDK + REST only.

Shared login roles are created via the Scaleway console before their migrations run; migrations only GRANT / REVOKE / set membership. Per-tenant roles are created by `provision_tenant`. A migration must apply on a fresh DB built by `scripts/db/apply-migrations.sh`; `scripts/replay-migrations.sh` replays on a fresh PG16 in CI on every PR touching `migrations/`. Fresh-environment / DR bootstrap and the exact role/grant state live in the private notes.

## Functions runner HMAC
- Gateway → runner traffic is HMAC-SHA256-signed with `FUNCTIONS_RUNNER_HMAC_SECRET` (≥32 bytes) in `eurobase-secrets`, read by both Deployments. Rotate by setting a new value and rolling both together.
- Enforcement via `FUNCTIONS_RUNNER_HMAC_REQUIRE_SIGNED` (`true` → strict; otherwise soft during a rollout window; an invalid signature is always 401). The gateway aborts startup if the secret is missing in production.

## Console passkey MFA (#621)
- WebAuthn relying party defaults to the `CONSOLE_URL` host (not `eurobase.app`, which tenant apps share). `WEBAUTHN_RP_ID` / `WEBAUTHN_RP_ORIGINS` override; changing the RP id orphans enrolled passkeys — treat as permanent.
- ≥1 row in `platform_passkey_credentials` = MFA on for that user: `SignIn` returns `mfa_required` + a single-use step-up token. Passkey sessions mint `login_via=passkey`.
- Both passkey tables are revoked from `eurobase_gateway` — passkey queries run on the developer pool; the gateway aborts startup if the developer pool is set but WebAuthn setup fails.
- Password reset clears all passkeys (same tx) + sends a notice email.

## Per-tenant function logins
- `FUNC_PASSWORD_SECRET` (≥32 bytes) in `eurobase-secrets` derives each tenant `<schema>_func` login password (`internal/tenantlogin`, mirrored in `functions-runner/tenant_db.ts`, pinned by a test vector).
- Worker (`tenantlogin.Ensurer`, startup + every 5 min, developer pool as migrator) keeps each `_func` role's LOGIN, connection limit, password and CONNECT in order, and `RESET ALL`.
- Cron `sql`/`rpc` jobs open a short-lived connection as `<schema>_func` per job (`cron.Executor.WithTenantLogins`); RLS identity per job via `cron_jobs.run_as`. The console Test Run (`cron.Executor.DryRun`) uses the same login/validation, always rolled back.
- **Team projects:** the runner resolves the live dedicated instance (`runner_get_tenant_db`) and connects directly as `<schema>_func` with a per-instance password; never falls back to the shared cluster.
- Runner: `ctx.db.sql` runs **only** on a `<schema>_func` connection (per-pod LRU cap); the runner's own login is used only for code/secret helpers. **No shared-connection fallback** and **never run customer SQL on the runner's own connection** — a failed login returns a retryable error. Budgets, rotation and the RDB-network follow-up are in the private notes.

## Connection pooling (#641)
- PgBouncer in transaction mode, two separate deployments: `pgbouncer` for the runner (tenant roles only, no platform secrets in the pod) and `pgbouncer-gateway` for the gateway (platform roles). The worker publishes each tenant's SCRAM verifier for the runner pooler; a compromised runner pooler gets no master secret.
- **No session state between requests:** every pool whose queries touch tenant tables resets each connection on release (`db.ResetSession`, a pgxpool `AfterRelease` hook: `DISCARD ALL` + `DeallocateAll`). **Any new pool that touches tenant tables must use `db.WithSessionReset()`.** The transaction-mode pooler needs its own reset (`server_reset_query_always`).
- **Parser-agreement rule:** the lexical guards refuse settings that change how the server tokenizes string literals; customer statements run over the extended protocol with `SET LOCAL standard_conforming_strings = on` so the server tokenizes the way the guards do.
- **Must stay on direct (non-pooled) connections:** golang-migrate, tenant `_ddl` migration connections, the worker's River `LISTEN`, and the export's `pg_dump`.
- `query.HasMultipleStatements` uses the shared tokenizer; where the server must enforce a single statement, use `PgConn.ExecParams` (the simple protocol runs every statement).

## Team-tier routing (epic #684)
- A project with a live `project_databases` row is served from its dedicated instance **or refused — never from the shared cluster**. `tenant.PlatformTenantContext` answers 503 when the owner pool can't be opened; console engines route via `query.TenantPoolFromContext`.
- **SDK** (`sdkTenantPoolMw` on `/v1/db`, `/v1/auth`, `/v1/vault`, `/v1/storage`, OAuth callbacks) puts a Team project's **runtime** pool on the request (never the owner — SDK stays under RLS), or 503. Storage, vault, edge functions, cron, background jobs and schema/DDL each resolve the dedicated pool the same way and fail closed rather than touch shared. **Never fall back to the shared cluster for a Team project.**
- Instance size, connection budget and the per-surface details are in the private notes. `scripts/test-team.sh` → `TestTeamEndToEnd` (CI `team-e2e.yml`) drives the real router + runner; open gaps are tracked in `teamKnownGaps` and a fix PR must remove its entry.

## Team-tier credentials
- `dbprovider.Cipher` seals the `project_databases` passwords; `RUNTIME_PASSWORD_SECRET` derives the dedicated `eurobase_gateway` login password per instance. Both in `eurobase-secrets`; empty is legal in dev. Key versions, rollout ordering and the rule to **never decrypt a legacy key_version 0 vault row from a customer-owned database** are in the private notes.

## Access control
- **Scoped personal access tokens (#702):** a token is bound to one project and one role (viewer / developer / admin), no expiry, prefix `eb_ptk_` (legacy `eb_pat_` = account-wide). `tenant.CallerProjectRole` is the single role function every access decision uses; `capSessionRole` caps a token to `min(token role, creator's current role)`. `auth.ScopedTokenAllowed` is deny-by-default. `TestNoDirectRoleLookups` forbids direct role lookups outside `tenant/access.go`.
- **Access grants need consent; tokens don't change access.** Org membership is accepted by the invitee (`org_invitations`, 30-day expiry); never insert `org_members` for another user. SSO sessions are scoped to their org. Personal access tokens carry `login_via=pat` and are refused on routes that change who can access what or the account itself.
- **Superadmin + SSO-required orgs:** superadmins never sign in via SSO; a superadmin **passkey** session (verified against the DB on every check — still superadmin, passkey > 24 h old) satisfies an org's `sso_required`, audited as `superadmin.sso_bypass`. Use `tenant.ClaimsSatisfySSOFor` for every new SSO check.

## Mollie billing
- Payment processor is Mollie (EU-headquartered). API keys in `eurobase-secrets` (`MOLLIE_API_KEY_TEST`/`_LIVE`, `MOLLIE_ENV`). Feature flag `BILLING_ENABLED` (handlers return 503 when off). Empty API key is legal (dev). Estonian VAT: below the threshold, invoices carry the §19 note, no VAT charged. Recurring flow: a first `sequenceType=first` payment establishes a mandate; the webhook then creates the subscription.

## Auth
- Custom auth in Go (email/password, magic links, OAuth). Anon key for public client access; service key for server-side only — never in client code.

## Build & Deploy
- **New code never starts before migrations.** CI applies Deployment manifests pinned to the currently-running image (`apply_pinned`); new images arrive only via `kubectl set image` after migrations run. Any new Deployment manifest must be applied through `apply_pinned`, and each Deployment must be defined in exactly one manifest.
- Push to `main` triggers CI/CD (GitHub Actions). **Don't run deploy scripts manually — commit and push.**

## Compliance / Sub-Processors
When adding a third-party data processor: (1) insert it into `sub_processors` (migration), (2) add feature detection in `internal/compliance/registry.go` → `resolveActiveFeatures()`, (3) link it to the feature in `service_dependencies` (same migration). This keeps the DPA report automatic.

## Compliance exports (#654)
- Exports read tenant tables through `compliance.ExportSource`: the worker's developer pool **as `eurobase_developer` itself** (no `SET ROLE`), in one `REPEATABLE READ READ ONLY` snapshot with `row_security = off` and `restrict_nonsystem_relation_kind = 'view, foreign-table'`. **Never read export data through a runtime role** (it silently drops tables with end-user RLS). Tables listed from `pg_catalog`; every table gets a file; credential columns (`query.SensitiveSystemColumns`) are redacted.
- Archives are written straight to the project bucket under `exports/<project-id>/` (`storage.ExportArchivePrefix`), treated as platform-managed by the storage handler (admin-only download; refused for edge functions). Schema section (tenant exports, `format_version` 2) via `pg_dump` run as the export login. Team-tier reads from the dedicated instance as owner. Timeouts and status bookkeeping are in the private notes.

## Storage object ownership
- SDK storage is scoped per end user by the `storage_objects` RLS policy (`is_service_role() OR uploaded_by = current_end_user_id()`): download/delete/list/upload all check ownership; a key is claimed before anything reaches S3. Console storage and edge functions are service role.
- **Shared folders (#697):** a tenant table `storage_shared_prefixes` (visibility authenticated|public), managed via `/platform/projects/{id}/storage-sharing`. Only developer files (`uploaded_by IS NULL`) in a shared folder are shared; end users can't add or overwrite files there. The data API is read-only on `storage_objects` / `storage_shared_prefixes` for non-service callers. **Any new tenant-schema table must be added to the platform-table lists** (console hidden sets, CLI, GDPR export, RLS audit, schema-change tracking).

## Customer-configured outbound connections
- Any connection to a host a customer typed in (custom SMTP today) goes through `internal/netguard`: `CheckHostName` on save and `DialPublic` at connect time (resolve, keep public addresses only, dial the checked address). Custom SMTP also needs a submission port (587/465/2525), STARTTLS or TLS, and a login — checked on save and every send. Edge functions can't deliver mail directly (`functions-deny-smtp` blocks TCP 25 to any destination).

## Auth email log (000135)
- The SDK auth email endpoints answer "OK" whether or not an email went out (no enumeration). `public.project_email_log` records what happened (sent / skipped + reason / failed); console: Auth → Email log (developer+). The **gateway role may only INSERT**; listing and cleanup run on the developer pool. Recipients masked; 14-day retention.

## SDK releases (`@eurobase/sdk`)
In order: (1) bump `sdk/js/package.json` + CHANGELOG; (2) tag the merge commit `sdk-vX.Y.Z` and push; (3) `npm publish` from `sdk/js`; (4) GitHub Release from the tag, marked Latest; (5) update version mentions that advertise the latest SDK (console docs, marketing site). Tag the CLI `cli-vX.Y.Z`.

## Sovereignty
- All infrastructure runs in the EU (France) on Scaleway. No US cloud services (AWS, GCP, Azure, Cloudflare, Stripe, Vercel).
