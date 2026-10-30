-- 000148_email_suppressions.up.sql
--
-- Addresses the platform stops sending non-essential mail to (bulk
-- announcements, the onboarding series) because mail to them failed
-- permanently. Filled by the worker from the email provider's failed-email
-- list; only addresses of platform accounts or allowlist entries are kept,
-- and a row is removed once neither exists any more. Hard bounces count
-- against the sending domain's reputation, so we don't keep retrying.
--
-- Platform table: the gateway role gets DML through the default
-- privileges set in 000037.

CREATE TABLE IF NOT EXISTS public.email_suppressions (
    email         TEXT        PRIMARY KEY CHECK (email = lower(email) AND email <> ''),
    reason        TEXT        NOT NULL CHECK (length(reason) <= 300),
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The onboarding series now also skips unverified addresses (after
-- waiting for verification) and suppressed ones.
ALTER TABLE public.drip_email_sends
    DROP CONSTRAINT IF EXISTS drip_email_sends_status_check;
ALTER TABLE public.drip_email_sends
    ADD CONSTRAINT drip_email_sends_status_check
        CHECK (status IN ('sent', 'skipped_opt_out', 'failed', 'skipped_unconfirmed', 'skipped_bounced'));
