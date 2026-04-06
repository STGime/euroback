DROP TABLE IF EXISTS public.edge_function_logs;
DROP TABLE IF EXISTS public.edge_functions;
ALTER TABLE public.plan_limits DROP COLUMN IF EXISTS edge_function_limit;
