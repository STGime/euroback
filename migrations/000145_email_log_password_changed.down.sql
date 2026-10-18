-- 000145_email_log_password_changed.down.sql
DELETE FROM public.project_email_log WHERE flow = 'password_changed';
ALTER TABLE public.project_email_log DROP CONSTRAINT IF EXISTS project_email_log_flow_check;
ALTER TABLE public.project_email_log ADD CONSTRAINT project_email_log_flow_check
    CHECK (flow IN ('verification', 'password_reset', 'magic_link'));
