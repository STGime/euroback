-- 000077_mailing_preferences.up.sql
--
-- Phase C of the public-beta launch plan (docs/public-beta-launch-plan.md).
-- Per-user opt-out for outbound platform mail (onboarding drip, beta
-- updates, usage alerts).
--
-- Categories match what the SendDripEmailJob checks + what the
-- unsubscribe endpoint accepts:
--
--   onboarding    — the 6-mail welcome drip (Phase C).
--   beta_updates  — periodic beta-update announcements (existing;
--                   footer retrofitted in the Phase C PR so recipients
--                   can opt out).
--   usage_alerts  — 80 % / 95 % quota warnings (existing feature;
--                   opt-out here since GDPR requires an out).
--   all           — nuclear-option opt-out; suppresses every category
--                   incl. any we add later. Transactional mail
--                   (verification / password reset / magic link) stays
--                   sender-controlled and is NEVER suppressed by this
--                   table — the tenant needs those to run their app.
--
-- Absence of a row = opted-in. Presence with opted_out_at IS NOT NULL
-- = opted out. Absence with opted_out_at IS NULL = opted in explicitly
-- (used when a user resubscribes after opting out). One row per
-- (user, category) — PK covers that.
--
-- FK to platform_users so a user delete cascades. No FK on the
-- category text column — it's a small enum controlled by the app;
-- adding new categories doesn't require a schema change.

BEGIN;

CREATE TABLE public.mailing_preferences (
    user_id       UUID        NOT NULL REFERENCES public.platform_users(id) ON DELETE CASCADE,
    category      TEXT        NOT NULL,
    opted_out_at  TIMESTAMPTZ,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, category)
);

-- Small allowlist CHECK to catch typos in the app code (e.g. a
-- worker that passes 'onboardng' would insert silently otherwise).
ALTER TABLE public.mailing_preferences
    ADD CONSTRAINT mailing_preferences_category_check
        CHECK (category IN ('onboarding', 'beta_updates', 'usage_alerts', 'all'));

COMMIT;
