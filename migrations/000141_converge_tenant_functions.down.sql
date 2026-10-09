-- Reverting the convergence of ownership is not safe to do blindly: once a
-- developer has edited (CREATE OR REPLACE) a reassigned function as `_ddl`,
-- it is _ddl-owned by intent, and forcing it back to the migrator could
-- break the project. So the down migration only drops the helper function;
-- it does NOT re-reassign objects back to the migrator. The per-object owner
-- is left as-is (reverting a specific project is a manual, reviewed op).
DROP FUNCTION IF EXISTS public.converge_tenant_functions(TEXT);
