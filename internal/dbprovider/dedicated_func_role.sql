-- ---------------------------------------------------------------------
-- Edge functions on the dedicated instance (#676).
--
-- Applied after dedicated_bootstrap.sql on every bootstrap, and again by
-- the worker's tenant-login pass (EnsureFuncRole) so instances bootstrapped
-- before #676 get it too. Idempotent.
--
-- ensure_tenant_func_role(schema) gives the functions runner its own
-- role here, shaped like the shared cluster's <schema>_func: USAGE on
-- the tenant schema, DML on its tables and sequences (now and — via
-- default privileges — any the owner creates later), EXECUTE on the RLS
-- helpers. The instance holds one tenant, so there is nothing else to
-- reach. NOLOGIN here: the worker's tenantlogin.Ensurer sets LOGIN,
-- CONNECTION LIMIT and the password derived from FUNC_PASSWORD_SECRET
-- (the same derivation as on the shared cluster), and the provider's
-- SetPrivilege grants CONNECT (SQL GRANT CONNECT is a no-op on
-- Scaleway's rdb database).
--
-- <schema>.runner_vault_get(name) is the runner's vault lookup here
-- (the shared cluster's public.vault_get_for_runner equivalent):
-- SECURITY DEFINER as the owner (vault_secrets' RLS admits only the
-- auth path), EXECUTE for the func role only. Returns the sealed secret;
-- the runner decrypts it as before — and refuses key_version < 1 here:
-- the customer owns this database and could plant a legacy row, and
-- version 0 is sealed with the raw master key, not a per-tenant one.
--
-- Idempotent; called by BootstrapDedicated on every bootstrap (also on
-- retries, where provision_tenant exits early).
-- ---------------------------------------------------------------------
CREATE OR REPLACE FUNCTION public.ensure_tenant_func_role(p_schema text)
RETURNS void
LANGUAGE plpgsql
SET search_path = public, pg_temp
AS $fn$
DECLARE
    v_role text := p_schema || '_func';
BEGIN
    IF p_schema !~ '^tenant_[0-9a-f_]+$' THEN
        RAISE EXCEPTION 'ensure_tenant_func_role: unexpected schema name %', p_schema;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = v_role) THEN
        EXECUTE format('CREATE ROLE %I NOLOGIN', v_role);
    END IF;
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO %I', p_schema, v_role);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA %I TO %I', p_schema, v_role);
    EXECUTE format('GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA %I TO %I', p_schema, v_role);
    EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE eurobase_owner IN SCHEMA %I GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO %I', p_schema, v_role);
    EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE eurobase_owner IN SCHEMA %I GRANT USAGE, SELECT ON SEQUENCES TO %I', p_schema, v_role);
    -- Same as the shared cluster's provision_tenant: every function in the
    -- tenant schema (customer helpers used by RLS policies included).
    EXECUTE format('GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA %I TO %I', p_schema, v_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION public.current_end_user_id() TO %I', v_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION public.is_service_role() TO %I', v_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION public.is_internal_auth_path() TO %I', v_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION public.uuid_generate_v4() TO %I', v_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION %I.auth_uid(), %I.auth_role(), %I.auth_email() TO %I',
                   p_schema, p_schema, p_schema, v_role);

    -- Lockdown: exactly the shared cluster's privileges. The provider's
    -- SetPrivilege (readwrite, which grants CONNECT) may add more — as
    -- Scaleway's readonly does for eurobase_readonly (LockdownReadonlyGrants)
    -- — so strip anything beyond DML + sequence USAGE/SELECT. Callers run
    -- this again after SetPrivilege. (Grants made by the provider's
    -- superuser are recorded with the owner as grantor, so the owner can
    -- revoke them.)
    EXECUTE format('REVOKE TRUNCATE, REFERENCES, TRIGGER ON ALL TABLES IN SCHEMA %I FROM %I', p_schema, v_role);
    EXECUTE format('REVOKE UPDATE ON ALL SEQUENCES IN SCHEMA %I FROM %I', p_schema, v_role);
    EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE eurobase_owner IN SCHEMA %I REVOKE TRUNCATE, REFERENCES, TRIGGER ON TABLES FROM %I', p_schema, v_role);
    EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE eurobase_owner IN SCHEMA %I REVOKE UPDATE ON SEQUENCES FROM %I', p_schema, v_role);
    EXECUTE format('REVOKE CREATE ON SCHEMA %I FROM %I', p_schema, v_role);
    EXECUTE format('REVOKE ALL ON ALL TABLES IN SCHEMA public FROM %I', v_role);

    EXECUTE format(
        'CREATE OR REPLACE FUNCTION %I.runner_vault_get(p_name text)
         RETURNS TABLE(encrypted bytea, nonce bytea, key_version smallint)
         LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $_$
           SELECT secret, nonce, key_version FROM %I.vault_secrets WHERE name = p_name
         $_$', p_schema, p_schema);
    -- SECURITY DEFINER convention: no PUBLIC EXECUTE; the func role only.
    EXECUTE format('REVOKE ALL ON FUNCTION %I.runner_vault_get(text) FROM PUBLIC', p_schema);
    EXECUTE format('GRANT EXECUTE ON FUNCTION %I.runner_vault_get(text) TO %I', p_schema, v_role);
END;
$fn$;

REVOKE EXECUTE ON FUNCTION public.ensure_tenant_func_role(text) FROM PUBLIC;
