-- 000129_export_completeness.up.sql
--
-- #654: an export records whether it is complete and what is missing, so
-- the API and console can show it instead of reporting plain success.
--   complete  true  = every table exported in full, every section read;
--             false = see warnings; NULL = exported before this migration.
--   warnings  JSON array of human-readable lines (a table that failed or
--             was truncated, a section that couldn't be read).
--
-- No explicit BEGIN/COMMIT: golang-migrate wraps each .up.sql in its own tx.

SET LOCAL lock_timeout = '5s';

ALTER TABLE public.export_requests
    ADD COLUMN IF NOT EXISTS complete boolean,
    ADD COLUMN IF NOT EXISTS warnings jsonb;
