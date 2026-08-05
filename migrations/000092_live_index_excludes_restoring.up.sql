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
-- two-instance restore workflow needs the OLD (state='active') and
-- SHADOW rows to co-exist for the duration of the restore — the
-- shadow enters as 'provisioning' (per InsertProvisioning), flips
-- through 'restoring', and only becomes 'active' inside the
-- cutover tx that atomically demotes the old row. Any state in
-- the original predicate except 'active' would collide with the
-- old row at some point during the restore lifecycle.
--
-- The right invariant is stricter: **"one *active* dedicated DB
-- per project"** — the serving/routing definition, not any live-
-- pipeline definition. Fresh provisioning is safe under this
-- narrower index because CreateProject never has a pre-existing
-- 'active' row to collide with (project is being created for the
-- first time), and the flip-to-active at the end of
-- ProvisionTeamDatabaseWorker.Work still uniquely-guards. Restore
-- is safe because the cutover tx demotes old before promoting new
-- inside the same tx (see restore_team_db.go).
--
-- Concurrent restores are still prevented via the separate unique
-- partial index on public.restore_operations, and concurrent
-- provisioning is prevented by the enqueue path itself
-- (CreateProject fires the worker once with a deterministic
-- idempotency key).

BEGIN;

DROP INDEX IF EXISTS public.ux_project_databases_live_one_per_project;

CREATE UNIQUE INDEX ux_project_databases_live_one_per_project
    ON public.project_databases(project_id)
    WHERE state = 'active'
      AND deleted_at IS NULL;

COMMIT;
