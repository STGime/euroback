-- Restore a timestamp on backfill rows. We can't recover the original
-- (since there never was one — the rows were synthesised), but using
-- `to_timestamp(0)` (1970-01-01) at least keeps the column non-NULL if
-- something downstream relied on that, while making the "this is fake"
-- nature obvious.

UPDATE public.schema_changes
SET created_at = to_timestamp(0)
WHERE detail->>'source' = 'backfill'
  AND created_at IS NULL;
