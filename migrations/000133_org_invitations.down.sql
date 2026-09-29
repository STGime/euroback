-- Restores the memberships 000133 turned into invitations (only those —
-- invitations created afterwards were never accepted and stay dropped).
INSERT INTO public.org_members (org_id, platform_user_id, role, invited_via)
SELECT org_id, platform_user_id, role, 'manual' FROM public.org_invitations
 WHERE from_membership
ON CONFLICT (org_id, platform_user_id) DO NOTHING;
DROP TABLE IF EXISTS public.org_invitations;
