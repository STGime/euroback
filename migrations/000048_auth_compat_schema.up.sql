-- 000048_auth_compat_schema.up.sql
--
-- Closes #77: Supabase-style RLS policies (USING (auth.uid() = user_id))
-- silently broke because Eurobase had no auth.uid(). Users who copy-pasted
-- a Supabase-flavoured CREATE FUNCTION auth.uid() into their migration got
-- a function that reads `request.jwt.claims` — a GUC the gateway never
-- populates — so policies always evaluated against NULL.
--
-- This migration adds a global `auth` schema with helpers that read the
-- same `app.end_user_*` GUCs the existing public.current_end_user_id() /
-- per-tenant auth_uid() chain reads. Both forms now work identically:
--
--   USING (auth.uid()  = user_id)   -- Supabase-style, schema-qualified
--   USING (auth_uid()  = user_id)   -- Eurobase native, search_path-resolved
--
-- The functions are STABLE / SECURITY INVOKER and access only session
-- GUCs already populated by the gateway via SET LOCAL — no privilege
-- escalation surface.

BEGIN;

CREATE SCHEMA IF NOT EXISTS auth;
ALTER SCHEMA auth OWNER TO eurobase_migrator;

-- ── auth.uid() ── current end-user UUID, NULL when no end-user context
--                  (matches public.current_end_user_id() exactly so any
--                  Supabase tutorial that uses auth.uid() Just Works).
CREATE OR REPLACE FUNCTION auth.uid() RETURNS uuid
    LANGUAGE sql STABLE AS $$
    SELECT public.current_end_user_id()
$$;

-- ── auth.role() ── PostgREST/Supabase-style role label:
--                     'service_role'  — service key calls (app.end_user_role='service')
--                     'authenticated' — end-user JWT present
--                     'anon'          — neither
--                   This is purely for policy ergonomics; Eurobase RLS
--                   should still prefer is_service_role() / auth.uid().
CREATE OR REPLACE FUNCTION auth.role() RETURNS text
    LANGUAGE sql STABLE AS $$
    SELECT CASE
        WHEN current_setting('app.end_user_role', true) = 'service' THEN 'service_role'
        WHEN public.current_end_user_id() IS NOT NULL THEN 'authenticated'
        ELSE 'anon'
    END
$$;

-- ── auth.email() ── current end-user email, NULL when not set.
CREATE OR REPLACE FUNCTION auth.email() RETURNS text
    LANGUAGE sql STABLE AS $$
    SELECT NULLIF(current_setting('app.end_user_email', true), '')
$$;

-- ── auth.jwt() ── synthetic claims object so policies that expect
--                  `auth.jwt() ->> 'sub'` (Supabase pattern) keep working.
--                  Keys present: sub, email, role.
CREATE OR REPLACE FUNCTION auth.jwt() RETURNS jsonb
    LANGUAGE sql STABLE AS $$
    SELECT jsonb_strip_nulls(jsonb_build_object(
        'sub',   public.current_end_user_id(),
        'email', NULLIF(current_setting('app.end_user_email', true), ''),
        'role',  auth.role()
    ))
$$;

ALTER FUNCTION auth.uid()   OWNER TO eurobase_migrator;
ALTER FUNCTION auth.role()  OWNER TO eurobase_migrator;
ALTER FUNCTION auth.email() OWNER TO eurobase_migrator;
ALTER FUNCTION auth.jwt()   OWNER TO eurobase_migrator;

-- The functions only read session GUCs and call already-public helpers;
-- they touch no privileged data. Grant USAGE + EXECUTE to every runtime
-- role so RLS policies that reference auth.* compile under any of them.
GRANT USAGE ON SCHEMA auth TO eurobase_gateway, eurobase_function_runner;
GRANT EXECUTE ON FUNCTION auth.uid()   TO eurobase_gateway, eurobase_function_runner;
GRANT EXECUTE ON FUNCTION auth.role()  TO eurobase_gateway, eurobase_function_runner;
GRANT EXECUTE ON FUNCTION auth.email() TO eurobase_gateway, eurobase_function_runner;
GRANT EXECUTE ON FUNCTION auth.jwt()   TO eurobase_gateway, eurobase_function_runner;

-- Per-tenant <schema>_func roles inherit through eurobase_function_runner
-- (000047 makes the runner a member of each), so the GRANT above covers
-- them. eurobase_developer inherits from eurobase_migrator (000044), so
-- developer/MCP traffic inherits ownership privileges and doesn't need
-- a separate grant.

COMMIT;
