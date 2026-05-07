-- 000046_rls_system_tables_service_only.down.sql
--
-- Reverts the policy tightening on existing tenants. Does NOT downgrade
-- provision_tenant (the migration system handles that by re-applying
-- 000040). Restores the permissive `USING (true)` policies that were
-- previously in place.
--
-- This is intentionally lossy: rolling back means accepting the
-- pre-fix cross-tenant leak vector from advisory GHSA-wcg9-846j-ch78.
-- Use only if you need to interoperate with code that depends on the
-- old permissive shape.

BEGIN;

DO $$
DECLARE
    v_schema TEXT;
    v_owner  TEXT;
    v_table  TEXT;
BEGIN
    FOR v_schema IN
        SELECT schema_name FROM public.projects WHERE schema_name IS NOT NULL
    LOOP
        FOREACH v_table IN ARRAY ARRAY['refresh_tokens', 'email_tokens', 'vault_secrets', 'user_identities']
        LOOP
            EXECUTE format(
                'SELECT tableowner FROM pg_tables WHERE schemaname = %L AND tablename = %L',
                v_schema, v_table
            ) INTO v_owner;
            IF v_owner IS NULL THEN
                CONTINUE;
            END IF;

            EXECUTE format('SET LOCAL ROLE %I', v_owner);

            CASE v_table
                WHEN 'refresh_tokens' THEN
                    EXECUTE format('DROP POLICY IF EXISTS refresh_tokens_policy ON %I.refresh_tokens', v_schema);
                    EXECUTE format('CREATE POLICY refresh_tokens_policy ON %I.refresh_tokens USING (true)', v_schema);
                WHEN 'email_tokens' THEN
                    EXECUTE format('DROP POLICY IF EXISTS email_tokens_policy ON %I.email_tokens', v_schema);
                    EXECUTE format('CREATE POLICY email_tokens_policy ON %I.email_tokens USING (true)', v_schema);
                WHEN 'vault_secrets' THEN
                    EXECUTE format('DROP POLICY IF EXISTS vault_secrets_policy ON %I.vault_secrets', v_schema);
                    EXECUTE format('CREATE POLICY vault_secrets_policy ON %I.vault_secrets USING (true)', v_schema);
                WHEN 'user_identities' THEN
                    EXECUTE format('DROP POLICY IF EXISTS user_identities_policy ON %I.user_identities', v_schema);
                    EXECUTE format('CREATE POLICY user_identities_policy ON %I.user_identities USING (true)', v_schema);
            END CASE;

            RESET ROLE;
        END LOOP;
    END LOOP;
END$$;

COMMIT;
