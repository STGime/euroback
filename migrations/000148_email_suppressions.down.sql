DELETE FROM public.drip_email_sends WHERE status IN ('skipped_unconfirmed', 'skipped_bounced');
ALTER TABLE public.drip_email_sends
    DROP CONSTRAINT IF EXISTS drip_email_sends_status_check;
ALTER TABLE public.drip_email_sends
    ADD CONSTRAINT drip_email_sends_status_check
        CHECK (status IN ('sent', 'skipped_opt_out', 'failed'));
DROP TABLE IF EXISTS public.email_suppressions;
