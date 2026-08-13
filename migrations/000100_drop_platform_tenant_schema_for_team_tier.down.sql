-- 000100_drop_platform_tenant_schema_for_team_tier.down.sql
--
-- Intentionally a no-op. Restoring the dropped tenant schemas would
-- require re-running provision_tenant() per Team-tier project (which
-- creates empty tables with sample data) — that does NOT restore any
-- data that lived in the dropped schema, so the "reversal" would be
-- misleading. If a rollback is ever needed, do it manually per
-- project after auditing what was there.

BEGIN;

DO $$
BEGIN
    RAISE NOTICE '000100 down: no-op — reversing this drop is not automatic; re-run provision_tenant() manually per project if you need empty stubs restored';
END $$;

COMMIT;
