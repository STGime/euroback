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
-- developer. CI applies this migration BEFORE the new gateway image (with
-- the flag) starts, so every such object is reassigned to `_ddl` first; the
-- only residual is the sub-second window where an old gateway pod (still
-- running as migrator) creates a table while draining, backstopped by 6d's
-- just-in-time reassignment.
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
