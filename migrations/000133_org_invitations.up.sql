-- 000133_org_invitations.up.sql
--
-- Org membership is accepted by the invitee. An org admin's invite
-- creates a pending row here; the org_members row is created when the
-- invitee accepts in the console, signed in with their own credentials.
-- A pending invitation grants nothing. Invitations expire after 30 days.
--
-- Existing memberships other than each org's creator are turned into
-- pending invitations (same role), so every member has accepted
-- membership explicitly. Orgs whose creator is gone or no longer a member
-- are left as they are (moving their members could leave no admin).
-- No explicit BEGIN/COMMIT: golang-migrate wraps the file.

CREATE TABLE IF NOT EXISTS public.org_invitations (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id           UUID NOT NULL REFERENCES public.organizations(id) ON DELETE CASCADE,
    platform_user_id UUID NOT NULL REFERENCES public.platform_users(id) ON DELETE CASCADE,
    role             TEXT NOT NULL CHECK (role IN ('admin', 'member')),
    invited_by       UUID REFERENCES public.platform_users(id) ON DELETE SET NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at       TIMESTAMPTZ NOT NULL DEFAULT now() + interval '30 days',
    -- true for the memberships this migration turned into invitations
    -- (the down migration restores only those).
    from_membership  BOOLEAN NOT NULL DEFAULT false,
    UNIQUE (org_id, platform_user_id)
);

CREATE INDEX IF NOT EXISTS ix_org_invitations_user ON public.org_invitations (platform_user_id);

-- Same rule as organizations / org_members (000114): developer pool only.
REVOKE ALL ON public.org_invitations FROM eurobase_gateway;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.org_invitations TO eurobase_developer;

-- Existing non-creator memberships → pending invitations.
WITH movable AS (
    SELECT m.org_id, m.platform_user_id, m.role, o.created_by
      FROM public.org_members m
      JOIN public.organizations o ON o.id = m.org_id
     WHERE o.created_by IS NOT NULL
       AND m.platform_user_id <> o.created_by
       AND EXISTS (SELECT 1 FROM public.org_members c
                    WHERE c.org_id = o.id AND c.platform_user_id = o.created_by)
), invited AS (
    INSERT INTO public.org_invitations (org_id, platform_user_id, role, invited_by, from_membership)
    SELECT org_id, platform_user_id, role, created_by, true FROM movable
    ON CONFLICT (org_id, platform_user_id) DO NOTHING
    RETURNING org_id, platform_user_id
)
DELETE FROM public.org_members m
 USING movable v
 WHERE m.org_id = v.org_id AND m.platform_user_id = v.platform_user_id;

COMMENT ON TABLE public.org_invitations IS
  'Pending org invitations (expire after 30 days). Membership (org_members) is created only when the invitee accepts, signed in to the console with their own credentials; a pending invitation grants nothing.';
