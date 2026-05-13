-- Migration History was stamping every pre-existing tenant table with
-- the current page-load time via the (since-removed) backfillUnloggedTables
-- function. Those rows are still in schema_changes with their fake
-- "freshly created" timestamps. Cleanup pass: any row tagged
-- detail.source = 'backfill' loses its bogus created_at. The console UI
-- already renders NULL created_at as "existed before tracking", and
-- under ORDER BY created_at DESC NULL sorts to the bottom (Postgres
-- default NULLS LAST), so the timeline ends up correct.
--
-- Idempotent: re-running NULLs already-NULL rows (no-op) and matches
-- any new backfill rows that snuck in between deploys.

UPDATE public.schema_changes
SET created_at = NULL
WHERE detail->>'source' = 'backfill'
  AND created_at IS NOT NULL;
