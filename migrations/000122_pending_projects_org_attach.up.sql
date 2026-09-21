-- 000122_pending_projects_org_attach.up.sql
--
-- Thread the onboarding-wizard "Owner" picker's choice through the
-- Mollie payment-first flow (issue #610). Before this, Pro-tier
-- projects always auto-attached to the caller's admin org because
-- pending_projects had no way to carry the picker choice across
-- the Mollie round-trip. PR #608 hid the picker on Pro as a UX
-- workaround; that guard comes out once this ships.
--
-- Three-state encoding matches the sync CreateProject path (see
-- CreateProjectRequest / OrgIDExplicit in internal/tenant/service.go):
--
--   org_id_explicit=false, org_id=NULL  → auto-attach at project-
--                                          creation time (webhook
--                                          picks caller's admin org
--                                          if any). Pre-#610 behaviour.
--   org_id_explicit=true,  org_id=NULL  → force personal (skip auto-
--                                          attach).
--   org_id_explicit=true,  org_id=UUID  → attach to that specific org.
--                                          Handler verified admin at
--                                          checkout-start time; the
--                                          webhook re-verifies before
--                                          the INSERT.
--
-- ON DELETE SET NULL handles the "org deleted between checkout and
-- webhook" race: FK cascade nulls org_id silently, and the webhook
-- reads org_id_explicit=true + org_id=NULL as "target was set but
-- is gone." Product decision (2026-09-21): treat that case as
-- personal + audit log (option A in the design conversation). The
-- alternative (refund the payment) was rejected because the target
-- org being deleted mid-payment is almost always the customer's
-- own org going away in a self-serve action; personal is the
-- smallest surprise and money stays in the door.

BEGIN;

ALTER TABLE public.pending_projects
    ADD COLUMN org_id          UUID    NULL REFERENCES public.organizations(id) ON DELETE SET NULL,
    ADD COLUMN org_id_explicit BOOLEAN NOT NULL DEFAULT false;

COMMENT ON COLUMN public.pending_projects.org_id IS
    'Target org for the project at checkout time. See migration 000122 for the three-state encoding: (explicit=false, org_id=NULL) means auto-attach, (true, NULL) means personal, (true, UUID) means attach to that org. ON DELETE SET NULL: if the target org is deleted before Mollie confirms payment, the null triggers the personal fallback (option A in the #610 design).';

COMMENT ON COLUMN public.pending_projects.org_id_explicit IS
    'True when the caller explicitly picked an owner in the wizard (org UUID or Personal). False (default) means the caller did not opt in and the webhook falls back to auto-attach. Matches CreateProjectRequest.OrgIDExplicit on the sync path.';

COMMIT;
