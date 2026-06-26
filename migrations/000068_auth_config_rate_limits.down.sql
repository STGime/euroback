-- 000068_auth_config_rate_limits.down.sql
--
-- The up migration was comment-only — no schema to revert. Existing rows
-- that have a `rate_limits` sub-object in `auth_config` stay where they
-- are; the runtime code on an earlier deploy simply ignores unknown JSON
-- keys (json.Unmarshal into AuthConfig without the field is a no-op).
SELECT 1 WHERE FALSE;
