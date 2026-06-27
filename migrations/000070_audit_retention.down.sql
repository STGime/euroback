-- 000070_audit_retention.down.sql

DROP FUNCTION IF EXISTS public.ensure_future_data_access_log_partitions(int);
DROP FUNCTION IF EXISTS public.drop_old_data_access_log_partitions(int);
DROP FUNCTION IF EXISTS public.prune_audit_log(int);
DROP TABLE    IF EXISTS public.audit_log_chain_checkpoints;
