-- Backfill USAGE on schema public for tenant `<schema>_func` roles.
--
-- The project's auth helpers (auth_uid(), auth_email()) are SQL functions
-- that call public.current_end_user_id() / public.* helpers, so whoever
-- evaluates an RLS policy using them needs USAGE on schema public at
-- runtime. provision_tenant grants it to new `_func` roles; the 000060
-- backfill could only take effect where eurobase_migrator held USAGE on
-- public WITH GRANT OPTION (otherwise a GRANT is a silent no-op), so some
-- roles created before that never got it. Since SDK data requests and
-- edge-function SQL run on `_func`, policies using those helpers then fail
-- with "permission denied for schema public" instead of filtering rows.
--
-- Grants it where missing and verifies; fails loudly if a grant had no
-- effect (missing grant option), naming the fix. The func keeper
-- (tenantlogin) keeps the grant from then on.

DO $$
DECLARE
    v_role    TEXT;
    v_missing TEXT[] := '{}';
BEGIN
    -- Only func roles that exist and lack the grant (checked by oid, so a
    -- project whose func role is gone can't make the check itself fail).
    FOR v_role IN
        SELECT r.rolname
          FROM public.projects p
          JOIN pg_roles r ON r.rolname = p.schema_name || '_func'
         WHERE NOT has_schema_privilege(r.oid, 'public', 'USAGE')
    LOOP
        EXECUTE format('GRANT USAGE ON SCHEMA public TO %I', v_role);
        IF NOT has_schema_privilege(v_role, 'public', 'USAGE') THEN
            v_missing := v_missing || v_role;
        END IF;
    END LOOP;
    IF cardinality(v_missing) > 0 THEN
        RAISE EXCEPTION 'USAGE on schema public could not be granted to % tenant func role(s), e.g. %; eurobase_migrator needs USAGE ON SCHEMA public WITH GRANT OPTION (granted by the cluster admin)',
            array_length(v_missing, 1), v_missing[1];
    END IF;
END$$;
