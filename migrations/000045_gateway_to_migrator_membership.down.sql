-- 000045_gateway_to_migrator_membership.down.sql
--
-- Reverse of 000045. Removes migrator's membership in gateway. After
-- this runs, the platform path will lose access to gateway-owned tables
-- in tenant schemas (the original bug); only roll back if the entire
-- developer-pool feature is being unwound.

BEGIN;

REVOKE eurobase_gateway FROM eurobase_migrator;

COMMIT;
