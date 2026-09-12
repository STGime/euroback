-- 000117_project_upgrades.up.sql
--
-- Free/Pro → Team tier upgrade path (issue TBD).
--
-- Team-tier projects live on their own Scaleway managed-PG instance.
-- Free/Pro projects share the platform cluster. Until now, "born
-- Team" was the only path — this migration adds the state machine
-- to move an existing shared-cluster project onto a dedicated
-- instance, copy all state across, cut the gateway over, and later
-- delete the source data.
--
-- Two additions:
--
--   * `public.project_upgrades` — one row per attempt. States drive
--     the River worker (internal/workers/upgrade_project.go). Unique
--     partial index on (project_id) WHERE state NOT IN
--     ('confirmed','failed') prevents concurrent upgrade jobs for
--     the same project.
--
--   * `public.projects.maintenance_mode` — read by the gateway
--     middleware during the cutover window (~30 seconds). When true,
--     SDK requests for that project return 503 with a JSON body so
--     writes don't land on the old cluster after routing has flipped.
--
-- Grants follow the #443-class REVOKE-then-GRANT pattern.
--   * project_upgrades: platform config, developer-pool only. Gateway
--     gets NOTHING — matches the org / support / contact tables.
--   * projects.maintenance_mode: gateway needs SELECT via the projects
--     table (already granted); no separate grant needed.
--
-- No explicit BEGIN/COMMIT: golang-migrate wraps each .up.sql tx.

CREATE TABLE public.project_upgrades (
    id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    -- ON DELETE RESTRICT — a live upgrade owns a Scaleway RDB
    -- instance via project_databases. Cascading the project delete
    -- through here would orphan the instance. Same reasoning as
    -- project_databases (000083).
    project_id          UUID        NOT NULL REFERENCES public.projects(id) ON DELETE RESTRICT,

    -- Snapshot the source + destination plan at request time. Kept
    -- for audit; the actual plan flip on `projects.plan` happens
    -- during the `cutting_over` state transition.
    from_plan           TEXT        NOT NULL,
    to_plan             TEXT        NOT NULL,

    -- State machine. Values (enforced by application layer, no CHECK
    -- constraint so we can add states without a migration):
    --   requested     — row created, worker not yet started
    --   provisioning  — Scaleway RDB provisioning in progress
    --   copying       — data being replicated to dedicated instance
    --   cutting_over  — maintenance_mode on; final sync + route swap
    --   live          — dedicated is authoritative; source data kept
    --                   as 7-day rollback buffer
    --   confirmed     — source data purged; upgrade fully closed
    --   failed        — terminal failure at any earlier step
    state               TEXT        NOT NULL,

    -- Link to the dedicated instance provisioned for this upgrade.
    -- Nullable because `requested` state exists before provisioning
    -- has run. SET NULL on project_databases delete so purging the
    -- underlying instance row doesn't cascade the upgrade record
    -- (kept for audit).
    project_database_id UUID        REFERENCES public.project_databases(id) ON DELETE SET NULL,

    -- Observability. bytes_copied lets the admin UI render a progress
    -- bar; also useful for post-hoc sanity checking.
    bytes_copied        BIGINT      NOT NULL DEFAULT 0,
    tables_copied       INT         NOT NULL DEFAULT 0,

    -- Lifecycle timestamps. NULL until the corresponding transition.
    started_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    provisioned_at      TIMESTAMPTZ,
    copied_at           TIMESTAMPTZ,
    cutover_at          TIMESTAMPTZ,
    confirmed_at        TIMESTAMPTZ,
    failed_at           TIMESTAMPTZ,

    -- On failure, the worker records the error text here so the
    -- admin UI can show it without spelunking the logs.
    error               TEXT,

    -- Admin who triggered the upgrade. NULL is legal (system-driven
    -- upgrades in the future, e.g. auto-upgrade after checkout).
    triggered_by        UUID        REFERENCES public.platform_users(id) ON DELETE SET NULL,

    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One active upgrade per project. Prevents concurrent worker runs
-- and prevents a stuck upgrade from being re-requested without an
-- explicit abort. Terminal states drop out so a project can be
-- upgraded again if a prior attempt failed.
CREATE UNIQUE INDEX ux_project_upgrades_active
    ON public.project_upgrades (project_id)
    WHERE state NOT IN ('confirmed', 'failed');

-- Sweeper needs a cheap "who's ready for cleanup" query.
CREATE INDEX ix_project_upgrades_live_cutover
    ON public.project_upgrades (cutover_at)
    WHERE state = 'live';

-- Admin UI: recent-first ordering.
CREATE INDEX ix_project_upgrades_started_desc
    ON public.project_upgrades (started_at DESC);

-- Auto-touch updated_at — mirror the pattern used on
-- project_databases (000083) and projects (000001).
CREATE OR REPLACE FUNCTION public.project_upgrades_touch_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- Convention (per CLAUDE.md #217 sweep): new secdef-adjacent
-- migrator-owned functions in public must revoke PUBLIC EXECUTE.
REVOKE EXECUTE ON FUNCTION public.project_upgrades_touch_updated_at() FROM PUBLIC;

CREATE TRIGGER trg_project_upgrades_touch_updated_at
    BEFORE UPDATE ON public.project_upgrades
    FOR EACH ROW
    EXECUTE FUNCTION public.project_upgrades_touch_updated_at();

-- ── Grants ─────────────────────────────────────────────────────
-- Same #443-class pitfall as organizations / support_requests: the
-- 000037 ALTER DEFAULT PRIVILEGES rule auto-grants gateway full DML
-- on every new migrator-owned public.* table. Upgrade rows carry
-- admin identity (triggered_by) and internal-only state — not
-- SDK-facing. REVOKE first, GRANT narrow (developer only).
REVOKE ALL ON public.project_upgrades FROM eurobase_gateway;
GRANT SELECT, INSERT, UPDATE ON public.project_upgrades TO eurobase_developer;

-- ── projects.maintenance_mode ─────────────────────────────────
-- Boolean read on every SDK request during a project's cutover
-- window. Default false so existing rows are unaffected. Application
-- layer flips true → sleep 5s → do final sync → route swap → flip
-- false. Duration typically ~30 seconds.
ALTER TABLE public.projects
    ADD COLUMN maintenance_mode BOOLEAN NOT NULL DEFAULT false;

COMMENT ON TABLE public.project_upgrades IS
  'Free/Pro → Team tier upgrade state machine. One row per attempt. See internal/workers/upgrade_project.go.';
COMMENT ON COLUMN public.project_upgrades.state IS
  'requested | provisioning | copying | cutting_over | live | confirmed | failed. See migration header.';
COMMENT ON COLUMN public.projects.maintenance_mode IS
  'When true, gateway returns 503 for all SDK requests for this project. Set during Team-tier upgrade cutover (~30 sec) and by ops in emergencies.';
