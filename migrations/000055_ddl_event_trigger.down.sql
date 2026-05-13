DROP EVENT TRIGGER IF EXISTS trg_log_ddl_end;
DROP EVENT TRIGGER IF EXISTS trg_log_ddl_drop;
DROP FUNCTION IF EXISTS public.log_ddl_event();
DROP FUNCTION IF EXISTS public.log_ddl_drop();
