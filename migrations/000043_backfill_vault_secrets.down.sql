-- 000043_backfill_vault_secrets.down.sql
-- Intentional no-op. The forward migration is purely additive
-- (CREATE TABLE IF NOT EXISTS + permissive policy + GRANT).
-- Dropping vault_secrets here would lose user-stored secrets on rollback.
SELECT 1;
