-- 000024_enable_pgtap.up.sql
-- Enable pgTAP extension for database testing (RLS policy testing, etc.)
-- This is a no-op if pgTAP is not installed on the server.

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_available_extensions WHERE name = 'pgtap'
    ) THEN
        CREATE EXTENSION IF NOT EXISTS pgtap;
    END IF;
END;
$$;
