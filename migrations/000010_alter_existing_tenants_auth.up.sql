-- 000010_alter_existing_tenants_auth.up.sql
-- Idempotently add auth columns and refresh_tokens table to existing tenant schemas.

BEGIN;

DO $$
DECLARE
    v_schema TEXT;
BEGIN
    FOR v_schema IN
        SELECT schema_name FROM public.projects WHERE status = 'active'
    LOOP
        -- Add auth columns to users table if missing
        EXECUTE format(
            'ALTER TABLE %I.users ADD COLUMN IF NOT EXISTS password_hash TEXT', v_schema
        );
        EXECUTE format(
            'ALTER TABLE %I.users ADD COLUMN IF NOT EXISTS email_confirmed_at TIMESTAMPTZ', v_schema
        );
        EXECUTE format(
            'ALTER TABLE %I.users ADD COLUMN IF NOT EXISTS last_sign_in_at TIMESTAMPTZ', v_schema
        );
        EXECUTE format(
            'ALTER TABLE %I.users ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ DEFAULT now()', v_schema
        );

        -- Make email UNIQUE NOT NULL (idempotent: skip if already constrained)
        BEGIN
            EXECUTE format(
                'ALTER TABLE %I.users ALTER COLUMN email SET NOT NULL', v_schema
            );
        EXCEPTION WHEN others THEN
            NULL; -- already NOT NULL
        END;

        -- Drop hanko_user_id column if it exists (no longer needed for end-users)
        EXECUTE format(
            'ALTER TABLE %I.users DROP COLUMN IF EXISTS hanko_user_id', v_schema
        );

        -- Create refresh_tokens table if not exists
        EXECUTE format(
            'CREATE TABLE IF NOT EXISTS %I.refresh_tokens (
                id         UUID        PRIMARY KEY DEFAULT public.uuid_generate_v4(),
                user_id    UUID        NOT NULL REFERENCES %I.users(id) ON DELETE CASCADE,
                token_hash TEXT        NOT NULL,
                expires_at TIMESTAMPTZ NOT NULL,
                revoked_at TIMESTAMPTZ,
                created_at TIMESTAMPTZ DEFAULT now()
            )',
            v_schema, v_schema
        );

        -- Create indexes (idempotent)
        EXECUTE format(
            'CREATE INDEX IF NOT EXISTS idx_refresh_tokens_token_hash ON %I.refresh_tokens(token_hash)',
            v_schema
        );
        EXECUTE format(
            'CREATE INDEX IF NOT EXISTS idx_refresh_tokens_user_id ON %I.refresh_tokens(user_id)',
            v_schema
        );

        -- Enable RLS on refresh_tokens
        EXECUTE format(
            'ALTER TABLE %I.refresh_tokens ENABLE ROW LEVEL SECURITY', v_schema
        );

        -- Add RLS policy (idempotent via IF NOT EXISTS check)
        BEGIN
            EXECUTE format(
                'CREATE POLICY refresh_tokens_policy ON %I.refresh_tokens USING (true)',
                v_schema
            );
        EXCEPTION WHEN duplicate_object THEN
            NULL;
        END;

        -- Update users RLS policy to use end_user_id
        BEGIN
            EXECUTE format('DROP POLICY IF EXISTS tenant_isolation_users ON %I.users', v_schema);
            EXECUTE format(
                'CREATE POLICY user_self_access ON %I.users
                    USING (id = current_setting(''app.end_user_id'', true)::uuid)',
                v_schema
            );
        EXCEPTION WHEN duplicate_object THEN
            NULL;
        END;
    END LOOP;
END;
$$;

COMMIT;
