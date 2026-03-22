-- 000010_alter_existing_tenants_auth.down.sql
-- No-op: we cannot safely remove columns from existing tenant schemas
-- without risking data loss.
BEGIN;
COMMIT;
