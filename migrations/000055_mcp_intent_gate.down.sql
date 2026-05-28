-- 000055_mcp_intent_gate.down.sql
--
-- Revert: restore policies to the pre-#164 form (USING is_service_role()).
-- WARNING: dropping this migration re-opens the MCP prompt-injection
-- exfiltration vector on refresh_tokens / email_tokens / vault_secrets.

DO $$
DECLARE
    rec RECORD;
BEGIN
    FOR rec IN SELECT schema_name FROM public.projects WHERE schema_name IS NOT NULL LOOP
        EXECUTE format('DROP POLICY IF EXISTS refresh_tokens_policy ON %I.refresh_tokens', rec.schema_name);
        EXECUTE format(
            'CREATE POLICY refresh_tokens_policy ON %I.refresh_tokens
             USING (public.is_service_role()) WITH CHECK (public.is_service_role())',
            rec.schema_name
        );
        EXECUTE format('DROP POLICY IF EXISTS email_tokens_policy ON %I.email_tokens', rec.schema_name);
        EXECUTE format(
            'CREATE POLICY email_tokens_policy ON %I.email_tokens
             USING (public.is_service_role()) WITH CHECK (public.is_service_role())',
            rec.schema_name
        );
        EXECUTE format('DROP POLICY IF EXISTS vault_secrets_policy ON %I.vault_secrets', rec.schema_name);
        EXECUTE format(
            'CREATE POLICY vault_secrets_policy ON %I.vault_secrets
             USING (public.is_service_role()) WITH CHECK (public.is_service_role())',
            rec.schema_name
        );
    END LOOP;
END$$;

DROP FUNCTION IF EXISTS public.is_internal_auth_path();
