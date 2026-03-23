-- Expand the CHECK constraint on schema_changes.action to support rename/alter operations.
ALTER TABLE public.schema_changes DROP CONSTRAINT schema_changes_action_check;
ALTER TABLE public.schema_changes ADD CONSTRAINT schema_changes_action_check
    CHECK (action IN ('create_table','drop_table','add_column','drop_column','rename_table','alter_column'));
