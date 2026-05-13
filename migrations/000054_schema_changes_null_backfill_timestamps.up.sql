-- Migration History was showing "Created table X" with TODAY's timestamp
-- for any pre-existing table that landed via SQL runner / MCP / direct DDL
-- (anything outside the platform's logged DDL handlers). Reason:
-- `backfillUnloggedTables` (internal/query/ddl_handler.go) runs on every
-- page load, inserts a stub `create_table` row for any table in
-- information_schema with no matching log entry, and lets `created_at`
-- default to `now()` — turning every page open into a false ledger entry.
--
-- The accompanying code change inserts NULL for `created_at` on
-- backfill rows going forward. This migration cleans up the bogus
-- "today at X:XX" stamps already in the table so existing rows render
-- as "Detected (existed before tracking)" instead of pretending to
-- have been created moments ago.
--
-- Identifies backfill rows via the existing detail marker
-- (`{"source":"backfill"}`) — that flag has been written since the
-- backfill was introduced, so this is precise and reversible.

UPDATE public.schema_changes
SET created_at = NULL
WHERE detail->>'source' = 'backfill';
