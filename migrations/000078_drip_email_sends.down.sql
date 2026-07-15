BEGIN;
DROP INDEX IF EXISTS public.idx_drip_email_sends_step_status;
DROP INDEX IF EXISTS public.idx_drip_email_sends_user_step;
DROP TABLE IF EXISTS public.drip_email_sends;
COMMIT;
