-- 000154_edge_function_limits.down.sql
UPDATE public.plan_limits SET edge_function_limit = 25  WHERE plan = 'pro';
UPDATE public.plan_limits SET edge_function_limit = 250 WHERE plan IN ('team', 'legal_team');
