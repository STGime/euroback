-- 000154_edge_function_limits.up.sql
--
-- Raise the per-project edge function count limits. A function that isn't
-- invoked costs a few rows; what costs resources is invocations, which
-- are bounded separately (rate limits, runner CPU / wall-time budgets,
-- connection caps). A low count cap mostly pushed developers who split
-- their backend into many small functions toward one large function.
--
--   free        3   (unchanged)
--   pro         25  -> 500
--   team        250 -> 0 (unlimited)
--   legal_team  250 -> 0 (unlimited)
--
-- 0 means unlimited: the create check (internal/functions/handler.go)
-- and the usage alerts only apply a limit > 0.

UPDATE public.plan_limits SET edge_function_limit = 500 WHERE plan = 'pro';
UPDATE public.plan_limits SET edge_function_limit = 0   WHERE plan IN ('team', 'legal_team');
