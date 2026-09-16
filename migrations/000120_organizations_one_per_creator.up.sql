-- Enforce the "one org per user" first-release policy in the database
-- so the guard is race-free.
--
-- The PR-593 CreateOrg check-then-INSERT in a READ COMMITTED tx is a
-- TOCTOU: two concurrent tx can both SELECT zero rows for
-- `created_by = uid` and both proceed to INSERT, leaving the user
-- owning two orgs. The rest of the plumbing (CreateProject
-- auto-attach picks one arbitrarily; console hides "New Organization"
-- once ownership is detected) then papers over the split state and
-- the user has no way to reconcile it.
--
-- Migration 000114 deliberately did not put a global UNIQUE on
-- `created_by` because multi-org is planned. A PARTIAL unique index
-- with the same predicate is:
--   * strong enough to make the guard deterministic (INSERT that
--     races the first raises 23505);
--   * scoped to the "created_by IS NOT NULL" shape we actually
--     care about;
--   * trivially droppable in the migration that ships multi-org
--     (see comment on 000114 line 47).
--
-- CreateOrg maps the 23505 SQLSTATE to ErrOrgAlreadyExists, so the
-- existing 409 handler translation and the console's fast-path
-- "you already own an organization" UX both continue to work.
CREATE UNIQUE INDEX IF NOT EXISTS uq_organizations_one_per_creator
    ON public.organizations (created_by)
    WHERE created_by IS NOT NULL;

COMMENT ON INDEX public.uq_organizations_one_per_creator IS
    'First-release policy: one org per user. Drop this index in the migration that ships multi-org (SKU on the roadmap).';
