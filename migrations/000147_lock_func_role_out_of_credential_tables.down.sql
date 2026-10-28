-- 000147 down: intentionally a no-op.
--
-- Re-granting the credential tables to the <schema>_func role would
-- reopen the account-takeover path this migration closes, and the func
-- role has no legitimate use for them. provision_tenant keeps the 000147
-- body (no credential-table grant to the func role). Same pattern as
-- 000127's down.
SELECT 1;
