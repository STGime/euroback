-- We can't recover the original creation times (they never existed —
-- the rows were synthesised by the buggy backfill). On rollback we
-- stamp them with the unix epoch so the column is non-NULL again
-- without claiming a current-day timestamp.

UPDATE public.schema_changes
SET created_at = to_timestamp(0)
WHERE detail->>'source' = 'backfill'
  AND created_at IS NULL;
