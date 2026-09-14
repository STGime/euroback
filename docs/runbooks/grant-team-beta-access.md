# Runbook: grant / revoke `team_beta_access`

Team tier is closed-beta gated by `platform_users.team_beta_access`.
This runbook is what ops does when a customer asks to try Team.

## Preconditions

- The customer already has a Free-tier account on the platform
  (they need a `platform_users` row before we can flip the flag).
- You have superadmin access on the console (your
  `platform_users.is_superadmin = true`).

If the customer hasn't signed up yet, send them
`console.eurobase.app/signup` first, then continue.

## Grant

### Console (preferred)

1. Log in to `console.eurobase.app` as a superadmin.
2. Go to **/admin** (superadmin-only, hidden from the nav).
3. Scroll to **Team-tier closed beta**.
4. Paste the customer's email into **Grant by email** → **Grant**.

The panel refreshes; the customer's row now appears in the table
with granted_at + granted_by = your account.

### Raw API (scripting / bulk grants)

```sh
# Look up the user's UUID from their email
curl -sSf "https://api.eurobase.app/platform/admin/users?email=${EMAIL}" \
  -H "Authorization: Bearer ${SUPERADMIN_JWT}" | jq -r '.users[0].id'

# Flip the flag
curl -sSf -X POST \
  "https://api.eurobase.app/platform/admin/team-beta/${USER_ID}" \
  -H "Authorization: Bearer ${SUPERADMIN_JWT}"
```

Endpoint is `POST /platform/admin/team-beta/{user_id}`. 204 on
success. Audit trail: `platform_users.team_beta_granted_at` +
`team_beta_granted_by` are set; an `audit_log` row with
`action = 'team_beta.granted'` is written by `writeAudit`.

## Verify the grant landed

The customer should now see, on next page load in the console:

- A **Team** plan option in the new-project modal.
- **Organizations** in the nav sidebar.
- **Support** in the nav sidebar.

You can also verify server-side:

```sh
curl -sSf "https://api.eurobase.app/platform/admin/team-beta" \
  -H "Authorization: Bearer ${SUPERADMIN_JWT}" | jq '.entries[] | select(.email=="'"$EMAIL"'")'
```

Expect: `granted_at` populated, `active_team_projects` = 0 (until
the customer creates one).

## What to send the customer next

A friendly reply along the lines of:

> Team-tier beta is enabled on your account. You'll see a new
> "Team" plan option when you create a project, and an
> "Organizations" tab in the sidebar for inviting teammates.
> Getting started: <link to docs/team-tier/getting-started.md>.
> Organizations + SSO setup: <link to
> docs/team-tier/organizations-sso.md>.
>
> The beta is free while we finalise paid billing. We'll notify
> you 30 days before we start charging so nothing surprises you.

Docs to send:

- [`docs/team-tier/getting-started.md`](../team-tier/getting-started.md)
- [`docs/team-tier/organizations-sso.md`](../team-tier/organizations-sso.md)

## Revoke

Prospective only — revoking does **not** tear down projects the
customer already created. If ops wants to reclaim the dedicated
Scaleway instances, delete the projects manually; the deprovision
worker sweeps them 7 days later.

### Before you revoke

**Check for solo-admin orgs.** If the customer solo-admins any
organization, revoking freezes that org — no one will be able to
update SSO config or invite members until Team beta is re-granted
to a co-admin or restored to the original user. This is
intentional (see PR #537 review round 1), but a surprise if you
don't know about it.

```sh
# Any orgs where this user is the ONLY admin?
psql "$DATABASE_URL_DEVELOPER" -c "
  SELECT o.id, o.name
    FROM public.organizations o
    JOIN public.org_members m ON m.org_id = o.id
   WHERE m.user_id = '${USER_ID}'::uuid
     AND m.role = 'admin'
     AND NOT EXISTS (
       SELECT 1 FROM public.org_members m2
        WHERE m2.org_id = o.id
          AND m2.role = 'admin'
          AND m2.user_id <> '${USER_ID}'::uuid
     );
"
```

If any rows come back, either grant Team beta to a co-admin first
or coordinate with the customer to hand off org ownership before
revoking.

### Console

Same `/admin` → **Team-tier closed beta** section. Click **Revoke**
on the row.

### Raw API

```sh
curl -sSf -X DELETE \
  "https://api.eurobase.app/platform/admin/team-beta/${USER_ID}" \
  -H "Authorization: Bearer ${SUPERADMIN_JWT}"
```

## When to decline

- Customer isn't EU-based and their use case is US-only. Team
  provisions Scaleway fr-par managed-PG; latency + data-residency
  posture only makes sense for EU-primary workloads.
- Customer wants to test the tier but has no Free/Pro project yet
  and can't describe a real workload. Point them at Free first;
  Team costs us Scaleway money per grant even during the free
  beta.
- Legal Team specifically (`legal_team_beta_access`) — that's a
  German legal-tech compliance SKU with a different sales
  process; do not grant it just because someone asked for Team.
  See [`docs/legal/v2/de-legal-tech-addendum.md`](../legal/v2/de-legal-tech-addendum.md).

## Related

- Handler: `internal/tenant/team_beta_admin_handlers.go`
- Routes: `internal/gateway/router.go:730-732`
- Gate check on project creation: `internal/tenant/service.go`
  (`UserHasTeamBetaAccess` before `CreateProject`)
- Admin console panel: `console/src/routes/(app)/admin/+page.svelte:660`
- Customer-facing getting-started: [`../team-tier/getting-started.md`](../team-tier/getting-started.md)
- OIDC SSO customer setup: [`../team-tier/organizations-sso.md`](../team-tier/organizations-sso.md)
