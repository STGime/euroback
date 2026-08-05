-- 000092_live_index_excludes_restoring.up.sql
--
-- M3 review blocker #2 fix.
--
-- The `ux_project_databases_live_one_per_project` index shipped in
-- migration 000083 originally covered
--
--   state IN ('provisioning', 'active', 'restoring')
--
-- with the intent of "one live instance per project". But the M3
-- two-instance restore workflow inserts the shadow instance in
-- state='restoring' *alongside* the old instance in state='active'
-- for the duration of the restore (potentially several minutes) —
-- both states are in the index predicate → 23505 unique_violation
-- at the InsertProvisioning step of every restore attempt.
--
-- The right behaviour is: the OLD row and the SHADOW row can
-- co-exist as long as the shadow is state='restoring'. Only at
-- cutover does the shadow flip to state='active' (in the same tx
-- that flips the old to state='deleting' + deleted_at=now()) — so
-- the "one live" invariant is preserved at every observable point
-- but the intermediate co-existence is allowed.
--
-- Fix: drop 'restoring' from the index predicate. Concurrent
-- restores are still prevented via the separate unique partial
-- index on public.restore_operations.

BEGIN;

DROP INDEX IF EXISTS public.ux_project_databases_live_one_per_project;

CREATE UNIQUE INDEX ux_project_databases_live_one_per_project
    ON public.project_databases(project_id)
    WHERE state IN ('provisioning', 'active')
      AND deleted_at IS NULL;

COMMIT;
