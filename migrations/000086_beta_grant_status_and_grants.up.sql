-- 000086_beta_grant_status_and_grants.up.sql
--
-- Team-tier M2 review fixes. Addresses three bugs surfaced by the
-- PR-#331 ultrareview:
--
--   bug_001 — RecordBetaGrant violates subscriptions_status_check
--             CHECK constraint. Widen the CHECK to include
--             'beta_grant', and widen the partial-unique index so
--             concurrent-checkout races are still caught.
--   bug_006 — DeprovisionTeamDatabaseWorker.HardDelete will fail
--             (SQLSTATE 42501) because migration 000083 only granted
--             SELECT/INSERT/UPDATE to eurobase_gateway. Add DELETE.
--   [also cleans up the misleading comment in 000083 by adjacency —
--    the follow-up fix in code lives in the tenant/service.go delete
--    path so ON DELETE RESTRICT works without requiring runtime
--    DELETE grants on projects → we DO want runtime DELETE on
--    project_databases though, because the worker owns lifecycle.]

BEGIN;

-- Widen subscriptions status enum to include 'beta_grant'. The old
-- constraint listed the paid-flow statuses only — we now need a
-- distinct label for closed-beta grants so the /billing screen
-- distinguishes them from paid subscriptions (and downstream code
-- like the downgrade cron ignores them).
ALTER TABLE public.subscriptions
    DROP CONSTRAINT IF EXISTS subscriptions_status_check;

ALTER TABLE public.subscriptions
    ADD CONSTRAINT subscriptions_status_check
        CHECK (status IN ('incomplete', 'active', 'past_due', 'canceled', 'expired', 'beta_grant'));

-- Widen the "one live sub per project" partial unique index. Without
-- this, RecordBetaGrant's docstring claim that the index catches
-- races is false — a concurrent Mollie checkout on the same project
-- could sneak past. beta_grant + incomplete/active/past_due should
-- all be treated as "live" for uniqueness.
DROP INDEX IF EXISTS public.idx_subscriptions_project_live;
CREATE UNIQUE INDEX idx_subscriptions_project_live
    ON public.subscriptions(project_id)
    WHERE status IN ('incomplete', 'active', 'past_due', 'beta_grant');

-- Grant DELETE on project_databases to the runtime role. The
-- DeprovisionTeamDatabaseWorker runs under eurobase_gateway (worker
-- pod's DATABASE_URL wires that role per CLAUDE.md); it calls
-- Repo.HardDelete after successful provider.Delete. Without this
-- grant every deprovision would 42501 and dead-letter after five
-- retries with the underlying Scaleway instance already destroyed.
--
-- The 000083 migration comment ("No delete from runtime") reflected
-- the intended isolation model but was factually wrong for the
-- deployment shape — this row-lifecycle IS a worker action, and the
-- worker IS a runtime role.
GRANT DELETE ON public.project_databases TO eurobase_gateway;

COMMIT;
