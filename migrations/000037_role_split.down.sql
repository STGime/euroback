-- 000037_role_split.down.sql
--
-- Revert the gateway's GRANTs. Leaves the roles themselves in place —
-- delete them via the Scaleway console if you really want them gone.
-- Does NOT restore the pre-000037 provision_tenant body because that
-- would mean duplicating the entire function here; if you need the old
-- function, re-run migration 000023's body manually.

BEGIN;

DO $$
DECLARE
    rec RECORD;
BEGIN
    FOR rec IN SELECT schema_name FROM public.projects WHERE schema_name IS NOT NULL LOOP
        EXECUTE format('REVOKE ALL ON ALL TABLES IN SCHEMA %I FROM eurobase_gateway', rec.schema_name);
        EXECUTE format('REVOKE ALL ON SCHEMA %I FROM eurobase_gateway', rec.schema_name);
        EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE eurobase_migrator IN SCHEMA %I REVOKE ALL ON TABLES FROM eurobase_gateway', rec.schema_name);
        EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE eurobase_gateway IN SCHEMA %I REVOKE ALL ON TABLES FROM eurobase_gateway', rec.schema_name);
    END LOOP;
END$$;

REVOKE ALL ON ALL TABLES IN SCHEMA public FROM eurobase_gateway;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM eurobase_gateway;
REVOKE ALL ON SCHEMA public FROM eurobase_gateway;
REVOKE EXECUTE ON FUNCTION public.provision_tenant(UUID, TEXT, TEXT) FROM eurobase_gateway;
REVOKE CONNECT ON DATABASE eurobase FROM eurobase_gateway;

ALTER DEFAULT PRIVILEGES FOR ROLE eurobase_migrator IN SCHEMA public REVOKE ALL ON TABLES FROM eurobase_gateway;
ALTER DEFAULT PRIVILEGES FOR ROLE eurobase_migrator IN SCHEMA public REVOKE ALL ON SEQUENCES FROM eurobase_gateway;

-- Return object ownership to eurobase_api so the original role can
-- resume DDL if the cutover is fully abandoned.
REASSIGN OWNED BY eurobase_migrator TO eurobase_api;

COMMIT;
