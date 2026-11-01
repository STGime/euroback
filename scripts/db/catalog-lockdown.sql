-- Catalog lockdown (superuser step). Run as the cluster's superuser
-- (_rdb_superadmin on Scaleway; the superuser stand-in in
-- scripts/db/apply-migrations.sh) AFTER migration 000149. Idempotent.
--
-- All tenant schemas of the shared cluster live in one database. This
-- limits the system catalogs (definitions, columns, statistics) to the
-- platform roles that already have owner-equivalent access to every
-- tenant (eurobase_migrator, eurobase_developer), instead of PUBLIC.
-- Tenant roles keep information_schema, whose views show only objects the
-- role has privileges on (its own schema).
--
-- Not covered (by design): object names remain resolvable from ids through
-- the reg* types (e.g. 12345::regclass), which Postgres doesn't
-- access-check, and information_schema lists routine signatures that
-- carry PUBLIC EXECUTE. Definitions, columns, owners and statistics are
-- limited.
--
-- Never put this in a migration: as eurobase_migrator these GRANT/REVOKEs
-- on superuser-owned objects are silent no-ops. Check with
-- scripts/verify-tenant-migration-isolation.sh (check 13).
-- Revert: scripts/db/catalog-lockdown-revert.sql.

DO $lockdown$
DECLARE
    o record;
BEGIN
    -- Catalog ACLs are per database: this must run in the platform's.
    IF current_database() <> 'eurobase' THEN
        RAISE EXCEPTION 'catalog-lockdown.sql must run in database eurobase, not %', current_database();
    END IF;

    -- 1. Catalog tables and views (pg_class, pg_proc, pg_policy,
    --    pg_stat_* views, …): platform roles only.
    FOR o IN SELECT c.oid::regclass AS rel
               FROM pg_catalog.pg_class c
              WHERE c.relnamespace = 'pg_catalog'::regnamespace
                AND c.relkind IN ('r', 'v', 'm')
                -- only what PUBLIC can read today (pg_authid etc. never
                -- were; pg_stat_activity is already revoked)
                AND EXISTS (SELECT 1 FROM pg_catalog.aclexplode(c.relacl) a
                             WHERE a.grantee = 0 AND a.privilege_type = 'SELECT')
    LOOP
        -- grant first: the revert script finds what to reopen by it
        EXECUTE format('GRANT SELECT ON %s TO eurobase_migrator, eurobase_developer', o.rel);
        IF o.rel::text ~ '^pg_stat' THEN
            -- statistics views stay available to monitoring roles
            EXECUTE format('GRANT SELECT ON %s TO pg_monitor', o.rel);
        END IF;
        EXECUTE format('REVOKE SELECT ON %s FROM PUBLIC', o.rel);
    END LOOP;

    -- 2. Functions that return an object's definition, comment, size or
    --    statistics from its id (no privilege check of their own), incl.
    --    the pg_stat_get_* family. pg_get_expr stays: it needs
    --    an expression tree, which only the revoked catalogs hold, and
    --    information_schema.columns uses it for column defaults.
    FOR o IN SELECT p.oid::regprocedure AS fn
               FROM pg_catalog.pg_proc p
              WHERE p.pronamespace = 'pg_catalog'::regnamespace
                AND (p.proname IN (
                    'pg_get_functiondef', 'pg_get_function_arguments',
                    'pg_get_function_identity_arguments', 'pg_get_function_result',
                    'pg_get_function_arg_default', 'pg_get_function_sqlbody',
                    'pg_get_viewdef', 'pg_get_ruledef', 'pg_get_triggerdef',
                    'pg_get_indexdef', 'pg_get_constraintdef',
                    'pg_get_partkeydef', 'pg_get_partition_constraintdef',
                    'pg_get_statisticsobjdef', 'pg_get_statisticsobjdef_columns',
                    'pg_get_statisticsobjdef_expressions',
                    'obj_description', 'col_description', 'shobj_description',
                    'pg_describe_object', 'pg_identify_object',
                    'pg_identify_object_as_address',
                    'pg_relation_size', 'pg_total_relation_size',
                    'pg_table_size', 'pg_indexes_size')
                     OR p.proname ~ '^pg_stat_get_')
                AND (p.proacl IS NULL OR EXISTS (
                     SELECT 1 FROM pg_catalog.aclexplode(p.proacl) a
                      WHERE a.grantee = 0 AND a.privilege_type = 'EXECUTE'))
    LOOP
        EXECUTE format('GRANT EXECUTE ON FUNCTION %s TO eurobase_migrator, eurobase_developer', o.fn);
        IF o.fn::text ~ '^pg_stat_get_' THEN
            EXECUTE format('GRANT EXECUTE ON FUNCTION %s TO pg_monitor', o.fn);
        END IF;
        EXECUTE format('REVOKE EXECUTE ON FUNCTION %s FROM PUBLIC', o.fn);
    END LOOP;
END
$lockdown$;

-- 3. The functions runner's driver (postgres.js) maps array types on
--    connect from pg_type: type ids only, no names.
GRANT SELECT (oid, typarray, typelem, typcategory) ON pg_catalog.pg_type TO PUBLIC;

-- 4. information_schema: platform roles, and each tenant role directly
--    (tenant roles stay members of nothing). eurobase_migrator gets the
--    grant option so provision_tenant_ddl_role (000149) can grant it to
--    new tenants.
REVOKE USAGE ON SCHEMA information_schema FROM PUBLIC;
GRANT USAGE ON SCHEMA information_schema TO eurobase_migrator WITH GRANT OPTION;
GRANT USAGE ON SCHEMA information_schema TO eurobase_developer;
DO $tenants$
DECLARE
    r record;
BEGIN
    FOR r IN SELECT rolname FROM pg_catalog.pg_roles
              WHERE rolname ~ '^tenant_[0-9a-f_]+_(ddl|func)$'
    LOOP
        EXECUTE format('GRANT USAGE ON SCHEMA information_schema TO %I', r.rolname);
    END LOOP;
END
$tenants$;
