-- Revert the CHECK constraint to original values.
ALTER TABLE public.schema_changes DROP CONSTRAINT schema_changes_action_check;
ALTER TABLE public.schema_changes ADD CONSTRAINT schema_changes_action_check
    CHECK (action IN ('create_table','drop_table','add_column','drop_column'));
