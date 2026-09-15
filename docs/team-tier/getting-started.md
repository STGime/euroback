# Team tier (closed beta) — Getting started

Team tier gives each project its own **dedicated Postgres instance** on
Scaleway managed-PG in the EU (fr-par), instead of the shared cluster
Free and Pro projects live on. It also unlocks organizations + OIDC
single-sign-on so a company can share access with employees under one
identity provider.

The tier is currently a **closed beta**: it's free for participating
customers while we finish paid billing wiring (Mollie subscription
plan-change is the last piece — see `docs/billing/stacked-pr-plan.md`).

## What's actually shipped today

These are live and self-serve in the console:

- **Dedicated Postgres instance** — one per Team project, isolated
  compute, EU-only. Provisioned in ~90 s to 4 min after project
  creation (15 min ceiling before the worker gives up).
- **Automatic daily backups** with 7-day retention.
- **On-demand snapshots** with an optional 64-char tag — create
  before a risky migration, restore from console. 1 self-serve
  restore per calendar month is included.
- **Organizations** — create an org, invite teammates by email,
  admin vs member role. Project ownership can be assigned to an org.
- **SSO (OIDC)** — Google Workspace, Microsoft Entra ID, Okta,
  Authentik, or any OIDC-conformant provider. Per-org config.

## What's on the roadmap, not in the beta

State this up front with beta customers so nobody plans around it:

- **SAML SSO** — planned. OIDC only for now.
- **Project-level RBAC** (Owner / Admin / Developer / Read-only).
  Today, org roles are org-scoped (`admin`, `member`) and don't grant
  granular per-project permissions.
- **Domain-based SSO auto-provisioning** — an org admin must invite
  each teammate by email manually. First-login auto-add by verified
  email domain requires DNS-TXT verification and is a future PR (see
  the SECURITY comment on migration `000114`).
- **Dev / staging / prod branches** and **preview function envs** —
  in the monetization proposal but not shipped. Do not promise these.
- **PITR** was removed in favour of snapshots — see
  `docs/runbooks/backup-pitr-test.md` for the reasoning. Restore
  granularity is per-snapshot, not per-second.
- **Team billing** — free during the closed beta. When Mollie plan-
  change lands, we'll notify each beta customer with 30 days' notice
  before turning on charging.

## Onboarding a beta customer — end to end

### 1. Grant beta access

`team_beta_access` on `platform_users` is the gate. Everything else
(create a Team project, create an org, configure SSO) is refused
until it's `true`.

Ops runbook: [`docs/runbooks/grant-team-beta-access.md`](../runbooks/grant-team-beta-access.md).

The customer will notice: a "Team" plan option in the new-project
modal, an "Organizations" nav entry, and a "Support" nav entry.

### 2. Customer creates a Team project

1. Console → **Projects** → **New project**.
2. Choose plan **Team**.
3. Region defaults to fr-par (Scaleway EU).
4. Submit.

Behind the scenes the gateway inserts the project row, records a
Team-beta subscription (billing UI shows the grant), and enqueues a
`ProvisionTeamDatabase` River job. The worker calls Scaleway to
provision a managed-PG instance, waits for `state=active` (10-sec
poll intervals, 10-min timeout per attempt), sets the backup
schedule, and bootstraps the `eurobase_gateway` runtime role on the
dedicated instance using a password derived from
`RUNTIME_PASSWORD_SECRET`.

Typical wall-clock to `active`: 90 seconds to 4 minutes.

If the worker fails or times out, the project sits in `provisioning`.
The customer sees no console progress bar today — check the worker
logs (`kubectl logs -l app=eurobase-gateway --tail=200`) if a
customer reports it's been >15 min.

### 3. Customer connects

Console → project → **Database → Connection**.

- Role dropdown: `readonly` or `readwrite`.
- The panel polls the `getConnectionState` endpoint every 5 s
  until `state === 'active'`, then reveals host / port / user /
  password / DSN for the chosen role.

The credentials are for the dedicated instance — not the shared
cluster. Point psql, migration tools, or the SDK's Postgres URL
directly at them.

### 4. Backups + on-demand snapshots

Console → project → **Database → Backups**.

- Snapshots list shows created-at, size, and tag (if the customer
  set one). Automatic daily backups appear here alongside manual
  snapshots.
- **Create Snapshot** modal takes an optional tag (max 64 chars).
  Use it to label pre-migration or pre-release safety snapshots.
- **Restore** on any snapshot opens a confirm modal, then a
  progress panel. The 1-restore-per-month quota is enforced by the
  backend; the console shows the current usage.

Retention is 7 days for Team; older snapshots roll off automatically.

### 5. Organizations + SSO

Covered in a separate walkthrough: [`docs/team-tier/organizations-sso.md`](organizations-sso.md).

TL;DR: the customer creates an org, configures OIDC on their IdP,
adds the redirect URL to the IdP, saves the config in the console.
Teammates first sign up as regular platform users, get invited by
the org admin, then sign in via SSO from `console.eurobase.app/login`.

## Support

Team-tier customers see a **Support** entry in the console nav
(gated on `team_beta_access`). It routes to the standard support
form. Prioritise Team-tier tickets — beta customers are inherently
high-signal.

## Known rough edges (beta)

- No progress indicator during dedicated-DB provisioning; customer
  just sees "provisioning" until it flips to "active".
- No self-serve billing yet — nothing shows up on Mollie until we
  wire plan-change (that's a separate milestone).
- New invitees must have **already signed up** as a regular platform
  user (Free tier is fine) before the org admin can add them.
  Otherwise the invite endpoint returns "no platform user with that
  email — user must sign up first". Once they've signed up, the
  invite endpoint sends them a notification email with the org name
  and a link to the SSO login (see the SSO doc for details).
- Downgrade path from Team → Free/Pro is not implemented. Once a
  project is on a dedicated instance, it stays there for the beta.
