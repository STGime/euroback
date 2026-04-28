-- 000041_platform_waitlist.up.sql
-- Capture signup attempts that were rejected by the platform_allowlist gate.
-- The signup response promises "you've been added to the waitlist" — this is
-- where they actually land. Reviewed periodically to decide who to invite.

CREATE TABLE IF NOT EXISTS public.platform_waitlist (
    email           TEXT PRIMARY KEY,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    attempts        INT         NOT NULL DEFAULT 1
);

CREATE INDEX IF NOT EXISTS idx_platform_waitlist_created_at
    ON public.platform_waitlist (created_at DESC);
