-- Edge functions: deploy-time TypeScript/ESM transpilation (closes #189).
--
-- `code` keeps the original developer source (shown in console / `ef get`);
-- `compiled_code` holds the esbuild output (type-stripped, CommonJS-shaped)
-- that the runner actually executes. NULL means the function predates the
-- transpile step — the runner falls back to `code`, which is exactly the
-- plain-JS contract those functions were written against.
--
-- No new grants needed: roles access this table via table-level grants
-- (migration 000037), which cover new columns automatically.
ALTER TABLE public.edge_functions ADD COLUMN compiled_code TEXT;

COMMENT ON COLUMN public.edge_functions.code IS
    'Original TypeScript/JavaScript source as deployed by the developer';
COMMENT ON COLUMN public.edge_functions.compiled_code IS
    'esbuild output (TS stripped, ESM->CommonJS) executed by the runner; NULL = pre-transpile function, runner falls back to code';
