-- No-op: the grant matches what provision_tenant gives every new tenant
-- func role; revoking it would break RLS policies that use the project's
-- auth helpers.
SELECT 1;
