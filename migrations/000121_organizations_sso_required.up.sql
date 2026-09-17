-- Per-org SSO enforcement flag.
--
-- Before this migration, orgs had an OIDC config (organizations.oidc_config
-- added in 000114), but SSO was an OPTION on the login page, not a POLICY on
-- the org. A user who was a member of an SSO-configured org could still log
-- in via email+password and reach every project the org owns — with reach
-- identical to the SSO login. That defeats the purpose of buying "SSO":
-- customers expect that if IT revokes an SSO account, Eurobase access
-- disappears too. Reported by the first Team-tier customer (qtune, 2026-09).
--
-- Adding a per-org boolean flag. When true, sessions minted from password
-- login (JWT claim login_via='password' or missing — pre-fix sessions) will
-- be refused at every org-owned access site: GetOrgForMember,
-- LoadProjectAccess (via-org path), and ListProjects (org-union filter).
-- Sessions minted from SSO (JWT claim login_via='sso' AND sso_org_id =
-- this org) satisfy the check.
--
-- Default false — existing orgs are unaffected until an admin explicitly
-- flips this on. Admin-toggleable via PATCH /platform/orgs/{id}/sso-required.
-- The console SSO panel will surface an obvious warning when enabling
-- ("password login for all members is refused effective immediately").
ALTER TABLE public.organizations
    ADD COLUMN sso_required BOOLEAN NOT NULL DEFAULT false;

COMMENT ON COLUMN public.organizations.sso_required IS
    'When true, members must authenticate via this org''s OIDC to access the org and its projects. Sessions from password login are refused at every org access site. Default false.';
