-- 000109_edge_function_log_lines.down.sql
--
-- Reverses the log_lines column addition. Any persisted log-line
-- payloads on rollback are lost — this is expected; the up
-- migration is additive and the down is a hard revert to the
-- pre-migration schema shape.

BEGIN;

ALTER TABLE public.edge_function_logs
    DROP COLUMN IF EXISTS log_lines;

COMMIT;
