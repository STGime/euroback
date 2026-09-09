-- 000114_organizations_and_sso.up.sql
--
-- Team-tier SSO MVP (#130). Adds the org-shape the SSO flow needs to
-- attach IdP config to + bind provisioned users into. Three pieces:
--
--   1. public.organizations — one row per customer org, holds the OIDC
--      config (issuer / client_id / client_secret_ref pointing at vault,
--      NEVER inline plaintext). primary_email_domain is nullable —
--      populated later once we auto-provision org membership from the
--      email domain of an SSO login. Nullable for MVP so an org can
--      exist without domain-based auto-add.
--   2. public.org_members — user ↔ org membership + role. role is a
--      2-state enum ('admin' | 'member') today; the CHECK is a
--      forward-compat anchor for a future 'owner' or 'billing_admin'
--      layer. invited_via tracks whether membership came from a manual
--      admin invite ('manual') or an SSO first-login auto-add ('sso') —
--      audit + retention conversations later care about this.
--   3. public.projects.org_id — nullable additive column. Existing
--      per-user projects (35 files / 18 queries key on owner_id today)
--      stay NULL forever. Org-owned projects set org_id at creation
--      time; the tenant.IsProjectAccessible helper unions the two
--      access paths.
--
-- DELIBERATELY NOT DONE HERE:
--   * No migration of existing owner_id → org_id. Plan is additive.
--   * No org-level billing. Mollie customer stays per-user until a
--     real Team customer needs org billing (Phase 2).
--   * No SAML surface. OIDC-only for MVP; SAML config would slot into
--     a sibling `saml_config` JSONB or a new sso_configs table when a
--     customer names an IdP.
--
-- No explicit BEGIN/COMMIT: golang-migrate wraps each .up.sql in its
-- own tx.

-- ── organizations ───────────────────────────────────────────────
CREATE TABLE public.organizations (
    id                     UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name                   TEXT NOT NULL,
    -- Email domain used for auto-provisioning: when an SSO callback
    -- arrives with email = alice@acme.com, we look up the org WHERE
    -- primary_email_domain = 'acme.com' to know which org to bind
    -- the new member into. Nullable because MVP allows an org that
    -- explicitly invites members without domain matching (small
    -- consultancies, multi-brand agencies).
    --
    -- SECURITY — domain-squatting attack surface (#534 review).
    -- The column has NO UNIQUE constraint, so two orgs can claim
    -- the same domain. That's deliberate at the schema layer
    -- (multi-brand orgs, mergers, and post-acquisition renaming
    -- would break under a global UNIQUE), BUT the follow-up
    -- HANDLER PR MUST enforce DNS proof before honouring a domain
    -- for SSO auto-provisioning:
    --   1. Admin sets primary_email_domain='acme.com'
    --   2. Handler emits a random TXT-record token
    --      (e.g. 'eurobase-verification=xxxxx')
    --   3. Domain stays UNVERIFIED (SSO auto-provisioning refuses)
    --      until DNS resolves the TXT record
    --   4. Once verified, an audit_log row + a verified_at column
    --      (new in a Phase-2 migration) marks the domain trusted.
    -- Without this the first attacker who registers an org can
    -- claim victim-corp.com and intercept SSO logins. This is a
    -- HANDLER-level requirement — do not merge the SSO handler PR
    -- without it.
    primary_email_domain   TEXT NULL,
    -- OIDC provider config as JSONB:
    --   {
    --     "provider": "google" | "okta" | "azure-ad" | "auth0" | ...,
    --     "issuer": "https://accounts.google.com",
    --     "client_id": "...apps.googleusercontent.com",
    --     "client_secret_ref": "vault://sso/oidc/<uuid>",
    --     "redirect_url": "https://console.eurobase.app/auth/sso/callback"
    --   }
    -- CRITICAL: client_secret_ref is a POINTER into internal/vault,
    -- never the plaintext secret. The AES-256-GCM-encrypted actual
    -- value lives in public.vault_secrets. A JSONB dump of this
    -- table therefore leaks NO usable IdP credential.
    oidc_config            JSONB NULL,
    -- Nullable + ON DELETE SET NULL: deleting the platform_user who
    -- created the org sets created_by to NULL rather than aborting
    -- the DELETE. The alternative — NOT NULL + ON DELETE RESTRICT —
    -- would block offboarding any staff member who has ever stood
    -- up an org until every org they created gets reassigned; that's
    -- a surprise for a normal user-deletion path and not what the
    -- created_by field is for. Losing "who created this" after
    -- deletion is acceptable — the audit_log carries actor info per
    -- write, so the org's provenance survives elsewhere.
    -- (#534 review: NOT NULL + SET NULL was contradictory —
    -- reviewer reproduced a DELETE-aborts-on-cascade on PG16.)
    created_by             UUID NULL REFERENCES public.platform_users(id) ON DELETE SET NULL,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Bounds match the handler validator (defence-in-depth against a
    -- bypassed validator wedging the table).
    CHECK (char_length(name) BETWEEN 1 AND 200),
    CHECK (primary_email_domain IS NULL OR char_length(primary_email_domain) BETWEEN 3 AND 253)
);

-- Domain lookup during SSO callback — one row per domain expected
-- but we don't UNIQUE-constrain to allow shared multi-domain orgs
-- later (would migrate to a separate org_domains table if the pattern
-- shows up). Partial index skips NULL rows entirely.
CREATE INDEX ix_organizations_primary_email_domain
    ON public.organizations (lower(primary_email_domain))
    WHERE primary_email_domain IS NOT NULL;

-- ── org_members ─────────────────────────────────────────────────
CREATE TABLE public.org_members (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id            UUID NOT NULL REFERENCES public.organizations(id) ON DELETE CASCADE,
    platform_user_id  UUID NOT NULL REFERENCES public.platform_users(id) ON DELETE CASCADE,
    -- 2-state today; keep the CHECK broad enough to add 'owner' or
    -- 'billing_admin' later without a re-check migration on existing
    -- rows.
    role              TEXT NOT NULL DEFAULT 'member'
                          CHECK (role IN ('admin', 'member')),
    -- Provenance of the membership row — matters for audit and for
    -- future GDPR-erasure conversations. 'sso' rows were created by
    -- the SSO callback path; 'manual' rows came from an admin invite.
    invited_via       TEXT NOT NULL
                          CHECK (invited_via IN ('manual', 'sso')),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (org_id, platform_user_id)
);

-- Ownership-check hot path: given a platform_user, list every org
-- they belong to + their role. INCLUDE keeps role in the index tuple
-- so tenant.IsProjectAccessible resolves without a table hit.
CREATE INDEX ix_org_members_user
    ON public.org_members (platform_user_id)
    INCLUDE (org_id, role);

-- ── projects.org_id ────────────────────────────────────────────
-- Nullable ADD COLUMN so existing rows land on NULL and no per-user
-- project regresses. Foreign key with ON DELETE SET NULL because
-- deleting an org shouldn't cascade-delete the projects (the org
-- owner can transfer/reclaim them; forcing a delete would surprise
-- customers). Application-layer policy decides whether an org
-- deletion should orphan or forbid.
ALTER TABLE public.projects
    ADD COLUMN org_id UUID NULL REFERENCES public.organizations(id) ON DELETE SET NULL;

CREATE INDEX ix_projects_org_id
    ON public.projects (org_id)
    WHERE org_id IS NOT NULL;

-- ── Grants ──────────────────────────────────────────────────────
-- Same #443-class pitfall as billing_profiles / contact_requests:
-- migration 000037 sets ALTER DEFAULT PRIVILEGES FOR ROLE
-- eurobase_migrator IN SCHEMA public GRANT SELECT, INSERT, UPDATE,
-- DELETE ON TABLES TO eurobase_gateway. Any new migrator-created
-- public.* table therefore auto-grants gateway full DML — so a
-- naked "GRANT SELECT, INSERT, UPDATE, DELETE" on organizations /
-- org_members would be redundant AND would leave a security property
-- the plan depends on ("org membership is platform config, NOT
-- SDK-facing; a runtime SQLi in the SDK pool cannot exfiltrate org
-- roster or read OIDC config") not holding.
--
-- Fix: REVOKE ALL from gateway first, then GRANT only what the
-- gateway pool actually needs (nothing here — orgs are read/written
-- exclusively from the platform-authenticated pool which runs as
-- eurobase_developer).
REVOKE ALL ON public.organizations FROM eurobase_gateway;
REVOKE ALL ON public.org_members   FROM eurobase_gateway;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.organizations TO eurobase_developer;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.org_members   TO eurobase_developer;

-- ── Comments ────────────────────────────────────────────────────
COMMENT ON TABLE public.organizations IS
  'Team-tier orgs. Attach OIDC config to sso_config (client_secret_ref points at vault, never plaintext). primary_email_domain drives SSO auto-provisioning.';
COMMENT ON COLUMN public.organizations.oidc_config IS
  'JSONB {provider, issuer, client_id, client_secret_ref, redirect_url}. client_secret_ref is a vault:// URI, never inline secret.';
COMMENT ON TABLE public.org_members IS
  'Membership + role in an org. invited_via distinguishes manual admin invites (''manual'') from SSO first-login auto-adds (''sso'') — audit relevance.';
COMMENT ON COLUMN public.projects.org_id IS
  'NULL = per-user project (owner_id path). Non-NULL = org-owned; access via tenant.IsProjectAccessible which unions owner_id + org_members membership.';
