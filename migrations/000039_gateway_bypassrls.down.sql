-- 000039_gateway_bypassrls.down.sql
-- No meaningful rollback: removing the `public.is_service_role() OR`
-- prefix from every policy would require reparsing expression trees
-- and is not worth the complexity. Platform admin paths would fail
-- again, but the safe state (RLS enforced) is preserved. If a true
-- rollback is needed, restore tenant schemas from backup and re-run
-- migrations up to 000038.

SELECT 1;
