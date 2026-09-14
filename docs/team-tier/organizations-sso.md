# Organizations + SSO (OIDC) — customer setup guide

Team-tier organizations let a company share access to Eurobase
projects under one identity provider. Once configured, teammates
sign in to `console.eurobase.app` with their work email — the
console redirects them to your IdP, gets an ID token back, and
issues a platform session against your org's member roster.

**Scope:** OIDC only. SAML is on the roadmap but not shipped.
Domain-based auto-provisioning (add any user whose email is at
`@yourcompany.com`) requires DNS-TXT verification and is also a
future PR — for now, an org admin must invite each teammate by
email.

## Prerequisites

- Your Eurobase account has `team_beta_access = true` (ask
  support if you're not sure — see the ops runbook).
- You have admin rights on the identity provider you want to use
  (Google Workspace, Microsoft Entra ID, Okta, Authentik, or any
  OIDC-conformant provider).

## Step 1 — Create an organization

1. Console → **Organizations** → **Create organization**.
2. Give it a name (e.g. "Acme Corp") and a slug (e.g. `acme`).
   The slug is used in URLs; keep it short and lowercase.
3. Submit.

Creating an org makes you an **admin** of that org and records a
manual invite for you as the first member.

## Step 2 — Register Eurobase as an OIDC client at your IdP

You need four things from the IdP side:

| Field | What it is |
|---|---|
| Issuer | The base URL of your IdP's OIDC discovery. |
| Client ID | Identifier for the "Eurobase" app you're registering. |
| Client secret | Password for the app registration (kept only server-side). |
| Redirect URL | Where the IdP sends the user back after login. |

**Redirect URL you register at the IdP** — must be exactly:

```
https://api.eurobase.app/platform/auth/sso/callback
```

(This is the platform callback, not a per-org URL. The org is
resolved from the OIDC `state` parameter, so a single redirect
URL works for every org.)

### Google Workspace walkthrough (reference)

1. Go to **Google Cloud Console** → **APIs & Services** →
   **Credentials**.
2. **Create Credentials → OAuth client ID**.
3. Application type: **Web application**. Name it "Eurobase".
4. **Authorized redirect URIs**:
   `https://api.eurobase.app/platform/auth/sso/callback`
5. Save. Copy the **Client ID** and **Client secret**.
6. Issuer for Google: `https://accounts.google.com`.

### Microsoft Entra ID (Azure AD)

1. **App registrations** → **New registration** → name it
   "Eurobase", account type "single tenant" (or as your policy
   dictates).
2. **Redirect URI**: Web →
   `https://api.eurobase.app/platform/auth/sso/callback`.
3. Copy the **Application (client) ID**.
4. **Certificates & secrets → New client secret** — copy the
   secret **immediately** (Azure won't show it again).
5. Issuer:
   `https://login.microsoftonline.com/<tenant-id>/v2.0`.

### Okta / Authentik / other OIDC providers

Same shape — register an OIDC application, set the redirect URI
to the platform callback above, note the issuer / client ID /
client secret.

## Step 3 — Configure the org in the console

Console → **Organizations** → select your org → **SSO** panel.

Fill in:

- **Provider**: `google`, `entra`, `okta`, `authentik`, or `custom`.
  This is a label; it doesn't change the login flow.
- **Issuer**: from the table above.
- **Client ID**: from your IdP registration.
- **Client secret**: from your IdP registration. The console never
  displays this back — the panel shows `•••••••• (set)` once
  saved. To rotate, paste a new value and save.
- **Redirect URL**: `https://api.eurobase.app/platform/auth/sso/callback`
  (must be `https://`; the backend rejects anything else).

Save. The org's `oidc_config` JSONB row is updated; the client
secret is sealed with AES-256-GCM before persistence.

## Step 4 — Invite teammates

For each teammate:

1. **Teammate signs up first.** Ask them to create a free
   Eurobase account at `console.eurobase.app` using their work
   email. This is a hard prerequisite — the org invite endpoint
   returns "no platform user with that email — user must sign up
   first" if they haven't.
2. **Org admin adds them.** Console → Organizations → your org →
   **Members** → **Add member** → paste their work email.
3. Choose role:
   - `admin` — can invite/remove members, edit SSO config, edit
     org settings.
   - `member` — read-only for org config; can access org-owned
     projects per project permissions.
4. Save.

No invite email is sent yet (roadmap). Tell the teammate
out-of-band that they've been added.

## Step 5 — Teammate signs in via SSO

1. Teammate opens `console.eurobase.app/login`.
2. Click **Sign in with SSO**.
3. Enter the work email that was invited.
4. Console POSTs `/platform/auth/sso/init` with the email; backend
   looks up the org by member email, builds the IdP authorization
   URL (with a signed state cookie + `login_hint`), redirects.
5. Teammate completes IdP login; IdP redirects to
   `https://api.eurobase.app/platform/auth/sso/callback`.
6. Backend validates the ID token, matches the email against the
   `org_members` roster, issues a platform JWT, and redirects
   back to `console.eurobase.app/login#access_token=…&sso=1`.
7. Console reads the fragment, stores the token, redirects to the
   projects page.

## Assigning projects to the org

Any project you own can be transferred to org ownership so all
org members can access it:

Console → project → **Settings → Ownership** → **Transfer to
organization** → pick the org.

Once transferred, project access follows org membership. Members
see the org badge on the project card in the sidebar.

## Rotating credentials

- **Client secret**: paste a new value in the SSO panel and save.
  Old sealed value is overwritten. Rotate at your IdP first, then
  paste the new secret before old sessions expire.
- **Issuer / client ID**: changing these effectively re-links the
  org to a different IdP or app. Existing platform sessions
  continue until they expire.

## Troubleshooting

**"no platform user with that email — user must sign up first"**
Teammate hasn't signed up on the console yet. Ask them to create
a free account with the same email, then retry the invite.

**"SSO sign-in failed" after IdP redirect**
Three common causes:

1. The IdP-returned email isn't in `org_members` (check exact
   spelling; case-insensitive; Gmail-canonical form is accepted
   but aliases with `+tag` are not).
2. The redirect URI at the IdP doesn't exactly match
   `https://api.eurobase.app/platform/auth/sso/callback`.
3. The client secret in the console doesn't match the current
   secret at the IdP.

Check gateway logs (`kubectl logs -l app=eurobase-gateway | grep
sso`) for the specific reason.

**Teammate sees "SSO requires a Team-tier organization to have
invited you"**
They've clicked SSO on the login page but no org has invited them
under that email. Add them via the members panel.

## Security notes

- The client secret is encrypted at rest with AES-256-GCM using
  the `ORG_OIDC_ENCRYPTION_KEY` gateway secret. Never persisted
  in plaintext.
- SSO login only ever authenticates a user who is **already in
  `org_members`**. There is no domain-based auto-add today — a
  malicious IdP that returns a valid ID token for a user we've
  never invited cannot get a platform session.
- Sessions issued via SSO have the same lifetime as normal
  password logins (24h access token, refresh via the standard
  flow).

## Reference

- Console UI: `console/src/routes/(app)/organizations/[id]/+page.svelte`
- Backend SSO handlers: `internal/auth/sso.go`
- Org CRUD + config: `internal/tenant/orgs.go`, `orgs_handlers.go`
- Migration: `migrations/000114_organizations_and_sso.up.sql`
- Ops runbook (grant `team_beta_access`): [`../runbooks/grant-team-beta-access.md`](../runbooks/grant-team-beta-access.md)
