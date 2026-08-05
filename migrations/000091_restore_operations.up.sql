-- 000091_restore_operations.up.sql
--
-- Team-tier M3 (backup + PITR). Records the state machine of a
-- restore operation — from user click through provisioning of the
-- new instance, verification, cutover, and rollback-window
-- retention of the old instance.
--
-- Design:
--
--   * A restore is a TWO-INSTANCE operation: we provision a NEW
--     dedicated instance from the snapshot / PITR target, verify
--     it, atomically cut over the project's routing to point at
--     the new instance, and keep the OLD one for a 7-day rollback
--     window (M1's deprovision sweeper deletes it after that).
--     `superseded_by` on project_databases (migration 000083) is
--     the cross-link.
--   * `kind` discriminates snapshot-based from PITR restores.
--   * `state` progresses through the machine in a deliberately
--     small enum:
--       pending      — job enqueued, not yet started
--       provisioning — provider building the new instance
--       verifying    — new instance ready; sanity checks running
--       cutover      — project routing being swapped
--       complete     — swap done; old instance in rollback window
--       failed       — terminal failure; new instance (if any) has
--                      been torn down; project stays on old
--   * Numbered 000091 to sit atop M3's 000090; M2b's in-flight
--     range (087-089) untouched.

BEGIN;

CREATE TABLE public.restore_operations (
    id                    UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id            UUID        NOT NULL REFERENCES public.projects(id) ON DELETE CASCADE,

    kind                  TEXT        NOT NULL,        -- 'snapshot' | 'pitr'
    source_ref            TEXT        NOT NULL,        -- snapshot ID or PITR target time (as text)
    target_time           TIMESTAMPTZ,                  -- PITR only; NULL for snapshot restores

    state                 TEXT        NOT NULL DEFAULT 'pending',

    -- The new instance being restored INTO (nullable — set once
    -- the row is inserted in provisioning state).
    new_instance_id       UUID        REFERENCES public.project_databases(id) ON DELETE SET NULL,
    -- The old instance being restored FROM / superseded — this
    -- one's `superseded_by` will point at new_instance_id after
    -- cutover.
    old_instance_id       UUID        NOT NULL REFERENCES public.project_databases(id) ON DELETE CASCADE,

    error                 TEXT,                         -- populated on state='failed'

    -- Requester bookkeeping so the console can show "restored by
    -- Alice at 14:30" in the operation history.
    requested_by          UUID        REFERENCES public.platform_users(id) ON DELETE SET NULL,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at          TIMESTAMPTZ
);

-- Console lists a project's restore ops newest-first.
CREATE INDEX ix_restore_operations_project_created
    ON public.restore_operations(project_id, created_at DESC);

-- One live restore at a time per project — prevents the console
-- from firing two concurrent restores. State 'complete'/'failed'
-- drops out of the index so historical rows don't block a fresh
-- attempt.
CREATE UNIQUE INDEX ux_restore_operations_live_one_per_project
    ON public.restore_operations(project_id)
    WHERE state IN ('pending', 'provisioning', 'verifying', 'cutover');

GRANT SELECT, INSERT, UPDATE ON public.restore_operations TO eurobase_gateway;
GRANT SELECT, INSERT, UPDATE ON public.restore_operations TO eurobase_developer;

COMMIT;
