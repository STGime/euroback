-- 000145_email_log_password_changed.up.sql
--
-- The "your password was changed" notice to end users (#822) is logged in
-- project_email_log like the other auth emails: allow its flow.
-- No explicit BEGIN/COMMIT: golang-migrate wraps the file.

ALTER TABLE public.project_email_log DROP CONSTRAINT IF EXISTS project_email_log_flow_check;
ALTER TABLE public.project_email_log ADD CONSTRAINT project_email_log_flow_check
    CHECK (flow IN ('verification', 'password_reset', 'magic_link', 'password_changed'));
