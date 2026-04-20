-- 000039_gateway_bypassrls.up.sql
--
-- Originally attempted `ALTER ROLE eurobase_gateway BYPASSRLS`, but
-- Scaleway managed Postgres rejects that attribute change ("ROLE
-- modification to SUPERUSER/privileged role not allowed"). BYPASSRLS
-- is not grantable on the managed tier.
--
-- Replacement strategy: back-fill every policy in every tenant schema
-- so that the USING / WITH CHECK expression starts with
-- `public.is_service_role() OR ...`. This covers user-created tables
-- that received old-style presets via ApplyPolicyPreset before the
-- service-role branch was added to the preset generator — including
-- this project's sleep_sessions, which was blocking the console's
-- table editor.
--
-- Idempotent: any policy whose expression already mentions
-- is_service_role is skipped. Permissive "USING (true)" policies
-- are also skipped (nothing to rewrite).

BEGIN;

DO $$
DECLARE
    r RECORD;
    cmd_kw TEXT;
    using_part TEXT;
    check_part TEXT;
    new_using TEXT;
    new_check TEXT;
BEGIN
    FOR r IN
        SELECT
            n.nspname AS sch,
            c.relname AS tbl,
            p.polname AS name,
            p.polcmd AS cmd,
            p.polpermissive AS permissive,
            pg_get_expr(p.polqual, p.polrelid) AS qual,
            pg_get_expr(p.polwithcheck, p.polrelid) AS withcheck
        FROM pg_policy p
        JOIN pg_class c ON p.polrelid = c.oid
        JOIN pg_namespace n ON c.relnamespace = n.oid
        WHERE n.nspname LIKE 'tenant_%'
    LOOP
        -- Skip policies that already reference is_service_role.
        IF (r.qual IS NOT NULL AND position('is_service_role' IN r.qual) > 0)
           OR (r.withcheck IS NOT NULL AND position('is_service_role' IN r.withcheck) > 0) THEN
            CONTINUE;
        END IF;

        -- Skip fully-permissive USING(true) / WITH CHECK(true).
        IF (r.qual IS NULL OR r.qual IN ('true', '(true)'))
           AND (r.withcheck IS NULL OR r.withcheck IN ('true', '(true)')) THEN
            CONTINUE;
        END IF;

        cmd_kw := CASE r.cmd
            WHEN 'r' THEN 'FOR SELECT'
            WHEN 'a' THEN 'FOR INSERT'
            WHEN 'w' THEN 'FOR UPDATE'
            WHEN 'd' THEN 'FOR DELETE'
            WHEN '*' THEN 'FOR ALL'
            ELSE ''
        END;

        using_part := '';
        check_part := '';
        IF r.qual IS NOT NULL THEN
            new_using := 'public.is_service_role() OR (' || r.qual || ')';
            using_part := format(' USING (%s)', new_using);
        END IF;
        IF r.withcheck IS NOT NULL THEN
            new_check := 'public.is_service_role() OR (' || r.withcheck || ')';
            check_part := format(' WITH CHECK (%s)', new_check);
        END IF;

        EXECUTE format('DROP POLICY %I ON %I.%I', r.name, r.sch, r.tbl);
        EXECUTE format('CREATE POLICY %I ON %I.%I %s%s%s',
            r.name, r.sch, r.tbl, cmd_kw, using_part, check_part);

        RAISE NOTICE 'rls backfill: rewrote % on %.%', r.name, r.sch, r.tbl;
    END LOOP;
END$$;

COMMIT;
