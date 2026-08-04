-- 000084_team_beta_access.up.sql
--
-- Team-tier M2 of the closed-beta plan (milestone #2, issue #307).
-- Adds the per-user gate for the Team-plan closed-beta program.
--
-- Design:
--
--   * Per-user, not per-project. A granted user can create MANY
--     Team-tier projects; each spins up its own dedicated managed-PG
--     instance (see migration 000083 + internal/dbprovider). This
--     mirrors the platform_allowlist pattern (migration 000036) —
--     small operator-managed list, no self-serve gate.
--
--   * `granted_at` + `granted_by` for auditability. When we retire
--     the beta and flip to paid Mollie checkout, these columns stay
--     for record-keeping. `granted_by` is nullable because early
--     grants may pre-date the audit column and to avoid a CHECK
--     that would block a self-grant during ops-bootstrap.
--
--   * Default false so grants are opt-in. The pricing-page CTA
--     stays "Coming soon" for unflagged users; only granted users
--     see "Create Team project".
--
-- The plan/tier ROW itself (`plan_limits.team`) lands in migration
-- 000085 — separate migration so a rollback of just the tier
-- structure doesn't lose the beta grant list.

BEGIN;

ALTER TABLE public.platform_users
    ADD COLUMN team_beta_access     BOOLEAN     NOT NULL DEFAULT false,
    ADD COLUMN team_beta_granted_at TIMESTAMPTZ,
    ADD COLUMN team_beta_granted_by UUID        REFERENCES public.platform_users(id) ON DELETE SET NULL;

-- Cheap index for the admin "who has beta access" query. Small
-- partial index (~O(dozen) rows for the beta window) — expected to
-- stay tiny for the duration of the closed beta.
CREATE INDEX ix_platform_users_team_beta
    ON public.platform_users(team_beta_granted_at DESC)
    WHERE team_beta_access = true;

COMMIT;
