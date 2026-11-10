-- eurobase_export_reader: lets compliance exports read tables with
-- FORCE ROW LEVEL SECURITY in full (#906).
--
-- The export reads tenant tables as eurobase_developer (owner-equivalent,
-- row_security = off). Owners skip RLS, except on a table with FORCE ROW
-- LEVEL SECURITY, where only BYPASSRLS does. For those tables the export
-- switches to this role inside its snapshot (SET LOCAL ROLE, undone right
-- after the read).
--
--   NOLOGIN            reached only via SET ROLE from eurobase_developer
--   BYPASSRLS          the one thing the developer role lacks for FORCE tables
--   pg_read_all_data   SELECT on every table, no write privilege anywhere
--   INHERIT FALSE      eurobase_developer does not gain BYPASSRLS or the
--                      read grant by itself; it has to SET ROLE explicitly
--
-- Creating a BYPASSRLS role and granting pg_read_all_data need a superuser
-- (on Scaleway: _rdb_superadmin), so this can't be a migration. Without the
-- role, exports report FORCE RLS tables as not exported, with the reason.
-- Idempotent.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'eurobase_export_reader') THEN
        CREATE ROLE eurobase_export_reader NOLOGIN BYPASSRLS;
    ELSE
        ALTER ROLE eurobase_export_reader NOLOGIN BYPASSRLS;
    END IF;
END$$;
-- Options spelled out: managed PostgreSQL defaults unspecified ones to FALSE.
GRANT pg_read_all_data TO eurobase_export_reader WITH INHERIT TRUE, SET FALSE;
GRANT eurobase_export_reader TO eurobase_developer WITH INHERIT FALSE, SET TRUE;

-- Check: prints one row, t|t|t.
SELECT r.rolbypassrls AS bypassrls,
       pg_has_role('eurobase_developer', 'eurobase_export_reader', 'SET') AS developer_can_set,
       NOT pg_has_role('eurobase_developer', 'eurobase_export_reader', 'USAGE') AS developer_does_not_inherit
  FROM pg_roles r WHERE r.rolname = 'eurobase_export_reader';
