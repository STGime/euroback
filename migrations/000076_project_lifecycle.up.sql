-- 000076_project_lifecycle.up.sql
--
-- Phase B of the public-beta launch plan (docs/public-beta-launch-plan.md).
-- Adds three columns to `projects` for the idle-pause + grandfather
-- system that lets us tighten the Free tier without breaking existing
-- beta users:
--
--   state              — 'active' | 'paused'. Free projects with no
--                        signed request in 30 days flip to 'paused'
--                        via the idle_pause cron. Wake-on-request
--                        middleware flips back to 'active' on the
--                        first request after a pause.
--
--   last_active_at     — bumped on every authenticated request by the
--                        subdomain middleware. The idle_pause cron
--                        reads this column to decide who to pause.
--                        Nullable so existing rows keep the "unknown
--                        idle time" semantics until the first request
--                        after this migration lands.
--
--   grandfathered_until — TIMESTAMPTZ. While in the future, the
--                        enforcement layer uses the OLD (pre-Phase-B)
--                        `plan_limits.free` cap values for this
--                        project so existing beta users aren't
--                        broken by the Free-tier tightening in
--                        migration 000075. Set to now() + 90 days
--                        for every existing free project by this
--                        migration; NULL for new signups.
--
-- Why 90 days: matches the plan's decision-locked-in #3. Long enough
-- that early advocates don't feel bait-and-switched; short enough
-- that we get honest usage data on the new caps within a quarter.
--
-- Rollout note (matches 000072 / 000075): the wake-on-request path
-- checks `state` on every subdomain-scoped request. If a gateway pod
-- rolls WITHOUT the middleware update (i.e. old-code + new-schema),
-- the requests still succeed — the middleware short-circuits on the
-- default state='active' + never-updated last_active_at behaviour.
-- Safe migration → deploy ordering.

BEGIN;

ALTER TABLE public.projects
    ADD COLUMN state                TEXT        NOT NULL DEFAULT 'active',
    ADD COLUMN last_active_at       TIMESTAMPTZ,
    ADD COLUMN grandfathered_until  TIMESTAMPTZ;

-- Enforce the small enum inline via CHECK — no separate type change
-- if we later add 'suspended' / 'archived'.
ALTER TABLE public.projects
    ADD CONSTRAINT projects_state_check
        CHECK (state IN ('active', 'paused'));

-- Grandfather every existing free project for 90 days. Pro projects
-- get NULL — they were never on the old caps to begin with (project-
-- tier caps for Pro didn't change; the tightening is all Free-side).
UPDATE public.projects
   SET grandfathered_until = now() + INTERVAL '90 days'
 WHERE plan = 'free';

-- Fast lookup: "which projects should the idle_pause cron consider?"
-- (Free tier + active + last_active_at < cutoff.)
CREATE INDEX idx_projects_idle_pause_candidates
    ON public.projects (last_active_at, plan, state)
    WHERE plan = 'free' AND state = 'active';

COMMIT;
