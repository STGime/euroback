-- Reverts scripts/db/catalog-lockdown.sql (superuser). Reopens to PUBLIC
-- exactly what the lockdown closed: the catalog relations and functions
-- it granted to eurobase_migrator. pg_stat_activity and
-- pg_stat_get_activity are handled by their own step (isolation check 12)
-- and are never reopened here; neither are pg_cancel_backend and
-- pg_terminate_backend (step 5 of the lockdown grants them to no one). The explicit grants to platform
-- and tenant roles are left (harmless). Run as the cluster's superuser.
DO $revert$
DECLARE
    o record;
BEGIN
    FOR o IN SELECT c.oid::regclass AS rel
               FROM pg_catalog.pg_class c
              WHERE c.relnamespace = 'pg_catalog'::regnamespace
                AND c.relkind IN ('r', 'v', 'm')
                AND c.relname <> 'pg_stat_activity'  -- own step (check 12), never reopened
                AND EXISTS (SELECT 1 FROM pg_catalog.aclexplode(c.relacl) a
                             WHERE a.grantee = 'eurobase_migrator'::regrole
                               AND a.privilege_type = 'SELECT')
    LOOP
        EXECUTE format('GRANT SELECT ON %s TO PUBLIC', o.rel);
    END LOOP;
    FOR o IN SELECT p.oid::regprocedure AS fn
               FROM pg_catalog.pg_proc p
              WHERE p.pronamespace = 'pg_catalog'::regnamespace
                AND p.proname <> 'pg_stat_get_activity'  -- own step (check 12), never reopened
                AND EXISTS (SELECT 1 FROM pg_catalog.aclexplode(p.proacl) a
                             WHERE a.grantee = 'eurobase_migrator'::regrole
                               AND a.privilege_type = 'EXECUTE')
    LOOP
        EXECUTE format('GRANT EXECUTE ON FUNCTION %s TO PUBLIC', o.fn);
    END LOOP;
END
$revert$;
GRANT USAGE ON SCHEMA information_schema TO PUBLIC;
