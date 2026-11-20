-- 000156_free_edge_function_limit.down.sql
UPDATE public.plan_limits SET edge_function_limit = 3 WHERE plan = 'free';
