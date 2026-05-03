-- 000043_eurobase_developer_role.down.sql
--
-- Reverse of 000043. Safe because eurobase_developer never owns any
-- objects directly — every CREATE on the platform pool runs under
-- SET LOCAL ROLE eurobase_migrator, so ownership flows to migrator.
-- Nothing to REASSIGN here.

BEGIN;

REVOKE eurobase_migrator FROM eurobase_developer;
REVOKE CONNECT ON DATABASE eurobase FROM eurobase_developer;

-- Note: we intentionally do NOT DROP ROLE here. The role is bootstrapped
-- via the Scaleway console (mirroring 000037's pattern); dropping it
-- belongs to the same out-of-band channel that created it.

COMMIT;
