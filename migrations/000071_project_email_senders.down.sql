-- 000071_project_email_senders.down.sql

DROP TRIGGER  IF EXISTS touch_project_email_senders_updated_at ON public.project_email_senders;
DROP FUNCTION IF EXISTS public.touch_project_email_senders_updated_at();
DROP TABLE    IF EXISTS public.project_email_senders;
