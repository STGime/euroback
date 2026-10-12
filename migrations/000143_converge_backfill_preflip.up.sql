-- 000143_converge_backfill_preflip.up.sql
--
-- Step 6e: final ownership convergence immediately before the
-- PLATFORM_DDL_LOGIN flip ships in the same deploy.
--
-- Why here: console / MCP / schema-DDL SQL still creates objects as
-- eurobase_migrator, so migrator-owned tenant tables keep accumulating
-- between convergence runs (000136, 000141). Once PLATFORM_DDL_LOGIN is on,
-- that path runs as `<schema>_ddl`, which can only ALTER/DROP objects it
-- owns — so any still-migrator-owned table would be un-editable by its
-- developer. This backfill converges the accumulated set.
--
-- ORDERING, honestly: the flip is driven by the PLATFORM_DDL_LOGIN *env var*,
-- and the flag-honoring code already ships in the running image. CI applies
-- the gateway env change (rolling flagged pods) in "Apply K8s manifests",
-- which runs BEFORE "Run database migrations" — so the flip does NOT strictly
-- follow this backfill; it races it for the duration of the migrate job.
-- During that window a developer's ALTER/DROP of a table created-as-migrator
-- since the last convergence can transiently fail with "must be owner"
-- (reads / writes / CREATE are unaffected). It is NOT a sub-second window.
-- The window is closed by running the convergence ops sweep immediately AFTER
-- the deploy completes (and it self-heals on any later convergence); post-flip
-- new tables are created as `_ddl`, so nothing new accumulates.
--
-- This only RE-RUNS the existing, already-deployed, reviewed convergence
-- functions (000136 converge_tenant_ownership, 000141 converge_tenant_functions).
-- It defines nothing new and changes no function body. Both are idempotent,
-- reassign only runtime-neutral objects (plain tables / invoker functions /
-- invoker views / standalone types), and LEAVE the platform system tables,
-- SECURITY DEFINER functions and owner-run views migrator-owned. No explicit
-- BEGIN/COMMIT — golang-migrate wraps the file.

DO $backfill$
DECLARE
    v_schema TEXT;
BEGIN
    FOR v_schema IN SELECT schema_name FROM public.projects WHERE schema_name IS NOT NULL LOOP
        PERFORM public.converge_tenant_ownership(v_schema);
        PERFORM public.converge_tenant_functions(v_schema);
    END LOOP;
END;
$backfill$;
