-- 000078_drip_email_sends.up.sql
--
-- Phase C of the public-beta launch plan (docs/public-beta-launch-plan.md).
-- Idempotency + audit trail for the onboarding drip series (6 emails
-- fired via River jobs at day 0 / 2 / 4 / 6 / 8 / 10 after signup).
--
-- Every worker invocation writes a row here — success, opt-out skip,
-- or failure — so:
--
--   - Retries can't double-send: worker checks for an existing row
--     for (user_id, step) with status='sent' or 'skipped_opt_out'
--     before doing any work.
--   - Support can answer "did user X get email N and when" by
--     reading a single row.
--   - Drop-off analysis: "which step correlates with opt-out?" is a
--     GROUP BY step on status='skipped_opt_out'.
--   - Failure investigation: WHERE status='failed' surfaces bounces
--     without digging through provider logs.
--
-- No UI on top of this table in the Phase C PR (per the plan). The
-- console SQL editor is enough for beta scale; a superadmin UI can
-- follow when we've seen a week of drip traffic and know what
-- filters are actually useful.

BEGIN;

CREATE TABLE public.drip_email_sends (
    id         UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id    UUID NOT NULL REFERENCES public.platform_users(id) ON DELETE CASCADE,
    step       INT  NOT NULL,   -- 0..5 for the six-mail drip; leaves room for future steps.
    status     TEXT NOT NULL,   -- 'sent' | 'skipped_opt_out' | 'failed'
    error      TEXT,            -- populated when status='failed'; nullable otherwise.
    sent_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, step, status)
);

-- Sanity: known status values only.
ALTER TABLE public.drip_email_sends
    ADD CONSTRAINT drip_email_sends_status_check
        CHECK (status IN ('sent', 'skipped_opt_out', 'failed'));

-- Fast lookup for the idempotency guard the worker runs before
-- each send: "did we already handle (user, step) with a terminal
-- status?" — 'sent' or 'skipped_opt_out' means done; 'failed' means
-- retry (River's own retry loop covers this).
CREATE INDEX idx_drip_email_sends_user_step
    ON public.drip_email_sends (user_id, step);

-- Analytics: "how far through the drip is user X?" and "at what
-- step do people drop off?" both benefit from step-first ordering.
CREATE INDEX idx_drip_email_sends_step_status
    ON public.drip_email_sends (step, status);

COMMIT;
