-- 000069_rate_limits_hourly_quota_keys.down.sql
--
-- No schema to revert; the counter keyspace is Redis-resident and
-- expires automatically on the hour. Rolling back to a pre-#227 deploy
-- simply stops checking the cap — the orphan Redis keys age out within
-- ≤ 1 hour. No cleanup needed.
SELECT 1 WHERE FALSE;
