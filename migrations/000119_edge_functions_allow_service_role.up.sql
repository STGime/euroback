-- Edge functions: per-function opt-in for ctx.db.asService().
--
-- With this column set, a JWT-verified invocation may call
-- ctx.db.asService().sql(...) inside the function. The runner still
-- executes the query under the same per-tenant <schema>_func role — only
-- the RLS GUC app.end_user_role is flipped from 'authenticated' to
-- 'service' for the duration of that one query. app.end_user_id remains
-- set to the verified JWT sub so tenant audit rows still capture the
-- acting user.
--
-- Opt-in is per-function so the elevation is visible in the deployment
-- diff (like verify_jwt): a reviewer sees allow_service_role=true on the
-- function that needs it and can reason about the write policy that
-- authorises it. Default false — existing functions are unaffected.
--
-- The cross-tenant fence does not depend on this flag: the connecting
-- Postgres role is still <schema>_func, granted only on its own tenant
-- schema. This flag only decides whether the RLS branch a tenant's own
-- policies take (is_service_role() / app.end_user_role='service') is
-- reachable from a JWT-verified function invocation.

ALTER TABLE public.edge_functions
  ADD COLUMN allow_service_role BOOLEAN NOT NULL DEFAULT false;

COMMENT ON COLUMN public.edge_functions.allow_service_role IS
    'When true, ctx.db.asService() is available inside this function and flips app.end_user_role to service for that query (Postgres role unchanged). Default false.';
