-- Reverse of 000127 is intentionally a no-op: re-granting the runner
-- membership in every tenant role would reopen the cross-tenant pivot,
-- and the runner no longer uses it. provision_tenant keeps the 000127
-- body (no runner grant).
SELECT 1;
