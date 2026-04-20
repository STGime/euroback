-- 000039_gateway_bypassrls.up.sql
--
-- Give the gateway runtime role the BYPASSRLS attribute so that
-- platform-admin code paths (console → /platform/projects/{id}/data/*,
-- users/storage CRUD, GDPR export) can opt out of row-level security
-- per-transaction via `SET LOCAL row_security = off`.
--
-- Why this is needed in addition to the `service` role bypass in 000038:
-- migration 000038 rewrote the two platform-hardcoded policies
-- (user_self_access on users, storage_owner_access on storage_objects)
-- and the provision_tenant function to include `public.is_service_role()
-- OR ...`. It did NOT back-fill policies on user-created tables that
-- were applied via ApplyPolicyPreset before that change. Those tables
-- still have policies like `user_id = auth_uid()` with no service-role
-- branch, which deny every platform-admin read.
--
-- BYPASSRLS is an opt-in bypass: by default row_security is 'on' and
-- policies still apply for eurobase_gateway (tenant SDK queries
-- continue to enforce isolation). Only when application code
-- deliberately calls `SET LOCAL row_security = off` does the bypass
-- activate — and that's scoped to the transaction.
--
-- No change to eurobase_migrator (already admin, owner of tables).
-- No change to eurobase_api (legacy role, scheduled for deletion).

BEGIN;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'eurobase_gateway') THEN
        EXECUTE 'ALTER ROLE eurobase_gateway BYPASSRLS';
    END IF;
END$$;

COMMIT;
