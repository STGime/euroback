-- Restore the pre-lockdown (insecure) state: PUBLIC EXECUTE on the
-- provisioning function.
GRANT EXECUTE ON FUNCTION public.provision_tenant_ddl_role(text) TO PUBLIC;
