-- Pending invitations become memberships again, as before 000133.
INSERT INTO public.org_members (org_id, platform_user_id, role, invited_via)
SELECT org_id, platform_user_id, role, 'manual' FROM public.org_invitations
ON CONFLICT (org_id, platform_user_id) DO NOTHING;
DROP TABLE IF EXISTS public.org_invitations;
