-- 000050_storage_objects_unique_key.down.sql
--
-- Reverse 000050: drop the unique index from existing tenants. The
-- provision_tenant function is left with the UNIQUE inline (rolling
-- it back to the 000047 body would require duplicating the entire
-- function again here for a hypothetical rollback we're unlikely to
-- exercise; a future migration can replace it if needed).

BEGIN;

DO $$
DECLARE
    rec RECORD;
BEGIN
    FOR rec IN SELECT schema_name FROM public.projects WHERE schema_name IS NOT NULL LOOP
        EXECUTE format('DROP INDEX IF EXISTS %I.storage_objects_key_unique', rec.schema_name);
    END LOOP;
END$$;

COMMIT;
