-- Reverts scripts/db/catalog-lockdown.sql (superuser). Reopens to PUBLIC
-- exactly what the lockdown closed: the catalog relations and functions
-- it granted to eurobase_migrator (nothing PUBLIC couldn't read before,
-- e.g. pg_stat_activity stays revoked). The explicit grants to platform
-- and tenant roles are left (harmless). Run as the cluster's superuser.
DO $revert$
DECLARE
    o record;
BEGIN
    FOR o IN SELECT c.oid::regclass AS rel
               FROM pg_catalog.pg_class c
              WHERE c.relnamespace = 'pg_catalog'::regnamespace
                AND c.relkind IN ('r', 'v', 'm')
                AND EXISTS (SELECT 1 FROM pg_catalog.aclexplode(c.relacl) a
                             WHERE a.grantee = 'eurobase_migrator'::regrole
                               AND a.privilege_type = 'SELECT')
    LOOP
        EXECUTE format('GRANT SELECT ON %s TO PUBLIC', o.rel);
    END LOOP;
    FOR o IN SELECT p.oid::regprocedure AS fn
               FROM pg_catalog.pg_proc p
              WHERE p.pronamespace = 'pg_catalog'::regnamespace
                AND EXISTS (SELECT 1 FROM pg_catalog.aclexplode(p.proacl) a
                             WHERE a.grantee = 'eurobase_migrator'::regrole
                               AND a.privilege_type = 'EXECUTE')
    LOOP
        EXECUTE format('GRANT EXECUTE ON FUNCTION %s TO PUBLIC', o.fn);
    END LOOP;
END
$revert$;
GRANT USAGE ON SCHEMA information_schema TO PUBLIC;
