-- 000043_backfill_vault_secrets.up.sql
-- Backfill vault_secrets to tenant schemas missing it.
--
-- forestdream (and likely a handful of other tenants provisioned before
-- 000023 or during a transient broken-provision_tenant window) never got
-- the vault_secrets table. The 000040 backfill DO block only restored
-- phone/user_identities/email_tokens — it did not touch vault.
--
-- Symptom: GET /platform/projects/<id>/vault returns 500 because
-- VaultService.List queries tenant_<schema>.vault_secrets, which
-- doesn't exist. Console renders a red "Internal server error"
-- banner on the otherwise-empty placeholder.
--
-- Fix: create the table + RLS + permissive policy + gateway grant
-- for every tenant schema, idempotently. Matches the exact shape
-- provision_tenant() creates for new tenants in 000040.

BEGIN;

DO $$
DECLARE
    rec RECORD;
BEGIN
    FOR rec IN SELECT schema_name FROM public.projects WHERE schema_name IS NOT NULL LOOP
        EXECUTE format(
            'CREATE TABLE IF NOT EXISTS %I.vault_secrets (
                id          UUID        PRIMARY KEY DEFAULT public.uuid_generate_v4(),
                name        TEXT        UNIQUE NOT NULL,
                secret      BYTEA       NOT NULL,
                nonce       BYTEA       NOT NULL,
                description TEXT        DEFAULT '''',
                created_at  TIMESTAMPTZ DEFAULT now(),
                updated_at  TIMESTAMPTZ DEFAULT now()
            )',
            rec.schema_name
        );

        -- Idempotent: Postgres no-ops ENABLE RLS if already on.
        EXECUTE format('ALTER TABLE %I.vault_secrets ENABLE ROW LEVEL SECURITY', rec.schema_name);

        -- Drop-if-exists then recreate so the policy converges to the
        -- canonical permissive form used by provision_tenant() — even
        -- if a stale policy from an earlier intermediate state exists.
        EXECUTE format('DROP POLICY IF EXISTS vault_secrets_policy ON %I.vault_secrets', rec.schema_name);
        EXECUTE format(
            'CREATE POLICY vault_secrets_policy ON %I.vault_secrets USING (true)',
            rec.schema_name
        );

        -- Gateway DML grant, matching the structure 000037/000040 apply
        -- to other tenant tables. Idempotent: GRANT is a no-op if the
        -- privilege is already present.
        EXECUTE format(
            'GRANT SELECT, INSERT, UPDATE, DELETE ON %I.vault_secrets TO eurobase_gateway',
            rec.schema_name
        );
    END LOOP;
END$$;

COMMIT;
