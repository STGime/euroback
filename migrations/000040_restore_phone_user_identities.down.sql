-- 000040_restore_phone_user_identities.down.sql
-- No meaningful rollback. The backfill is purely additive
-- (columns + a new table + a CHECK constraint expansion), and
-- reversing it would drop tenant data that the application now
-- assumes exists. Intentionally a no-op.

SELECT 1;
