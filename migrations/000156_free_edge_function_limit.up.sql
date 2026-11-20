-- 000156_free_edge_function_limit.up.sql
--
-- Free projects: 3 -> 50 edge functions. As 000154 explains, what costs
-- resources is invocations (bounded by rate limits, runner CPU / wall-time
-- budgets and connection caps), not the number of deployed functions; 3
-- was too few to try a real app or a migration (a typical Supabase project
-- has more). Pro stays at 500, Team / Legal Team unlimited (0).

UPDATE public.plan_limits SET edge_function_limit = 50 WHERE plan = 'free';
