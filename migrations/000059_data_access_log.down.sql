-- 000059_data_access_log.down.sql
--
-- Reverses 000059. Dropping the partitioned parent cascades to every monthly
-- partition and the default partition, so they need no individual DROPs.

DROP TABLE IF EXISTS public.data_access_log CASCADE;
DROP FUNCTION IF EXISTS public.ensure_data_access_log_partition(date);
