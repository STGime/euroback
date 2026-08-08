-- 000096_prune_audit_log_by_plan.up.sql
--
-- Team-tier M2b (#317): plan-tier-aware audit-log pruning.
--
-- The existing prune_audit_log(cutoff_days) applies ONE cutoff to every
-- project's chain — a flat env-var value the worker reads at startup.
-- That means `plan_limits.audit_log_retention_days` (365 for team,
-- 3650 for legal_team, 0 for free/pro) is loaded into Go structs and
-- then ignored by the pruner. The gap surfaced when we set
-- legal_team = 3650 for §257 HGB / §147 AO compliance and it made no
-- observable difference in prod — the flat cutoff wins.
--
-- This function fixes that by pulling the per-project cutoff from the
-- project's plan tier. A caller-supplied `fallback_days` covers the
-- edge cases:
--   * project.plan not found in plan_limits (shouldn't happen but be
--     defensive) → fallback_days
--   * plan_limits.audit_log_retention_days = 0 (free / pro today) →
--     fallback_days
--   * global chain (project_id IS NULL — platform-level events not
--     tied to any project) → fallback_days
--
-- fallback_days = 0 means "skip these" (matches the old
-- prune_audit_log(0) semantics — never prune when zero).
--
-- Per-chain semantics inside the loop are unchanged from
-- prune_audit_log: advisory-lock the chain, keep the head, checkpoint
-- the last pruned (seq, row_hash). Multi-pod safety inherited.

BEGIN;

CREATE OR REPLACE FUNCTION public.prune_audit_log_by_plan(fallback_days int)
RETURNS TABLE(plan text, project_id uuid, rows_deleted bigint)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    r          RECORD;
    v_cutoff   timestamptz;
    v_deleted  bigint;
    v_last_seq bigint;
    v_last_h   bytea;
    v_chainkey text;
    v_days     int;
BEGIN
    -- Group by project (plus the __global__ chain for project_id
    -- IS NULL) and resolve the per-project cutoff via plan_limits.
    --
    -- The head_seq subselect is sampled BEFORE the advisory lock —
    -- safe under concurrent writers, same reasoning as
    -- prune_audit_log's inline comment: a stale head only causes
    -- under-pruning, never head loss.
    FOR r IN
        SELECT
            COALESCE(a.project_id::text, '__global__') AS chain_key,
            a.project_id                                AS project_id,
            COALESCE(p.plan, '__none__')                AS plan_code,
            COALESCE(pl.audit_log_retention_days, 0)    AS plan_days,
            (SELECT max(seq) FROM public.audit_log a2
              WHERE a2.project_id IS NOT DISTINCT FROM a.project_id) AS head_seq
        FROM public.audit_log a
        LEFT JOIN public.projects     p  ON p.id   = a.project_id
        LEFT JOIN public.plan_limits  pl ON pl.plan = p.plan
        GROUP BY a.project_id, p.plan, pl.audit_log_retention_days
    LOOP
        -- Cutoff resolution: per-plan value if the tier sets one, else
        -- caller's fallback. A zero cutoff means "skip this chain."
        v_days := r.plan_days;
        IF v_days IS NULL OR v_days <= 0 THEN
            v_days := fallback_days;
        END IF;
        IF v_days IS NULL OR v_days <= 0 THEN
            CONTINUE;
        END IF;
        v_cutoff := now() - make_interval(days => v_days);

        v_chainkey := r.chain_key;
        PERFORM pg_advisory_xact_lock(hashtext(v_chainkey));

        SELECT seq, row_hash
          INTO v_last_seq, v_last_h
          FROM public.audit_log
         WHERE project_id IS NOT DISTINCT FROM r.project_id
           AND created_at < v_cutoff
           AND seq < r.head_seq
         ORDER BY seq DESC
         LIMIT 1;

        IF v_last_seq IS NULL THEN
            CONTINUE;
        END IF;

        DELETE FROM public.audit_log
         WHERE project_id IS NOT DISTINCT FROM r.project_id
           AND seq <= v_last_seq;
        GET DIAGNOSTICS v_deleted = ROW_COUNT;

        INSERT INTO public.audit_log_chain_checkpoints
            (project_id, last_pruned_seq, last_row_hash, updated_at)
        VALUES
            (COALESCE(r.project_id, '00000000-0000-0000-0000-000000000000'::uuid),
             v_last_seq, v_last_h, now())
        ON CONFLICT (project_id) DO UPDATE
           SET last_pruned_seq = EXCLUDED.last_pruned_seq,
               last_row_hash   = EXCLUDED.last_row_hash,
               updated_at      = EXCLUDED.updated_at;

        -- Emit a row per chain we actually pruned. The worker
        -- aggregates for logging so ops sees per-tier counts.
        plan         := r.plan_code;
        project_id   := r.project_id;
        rows_deleted := v_deleted;
        RETURN NEXT;
    END LOOP;
    RETURN;
END$$;

ALTER FUNCTION public.prune_audit_log_by_plan(int) OWNER TO eurobase_migrator;
-- Per #217 lockdown convention: revoke PUBLIC, grant only to the
-- roles that call it. Only the worker (eurobase_gateway) invokes.
REVOKE EXECUTE ON FUNCTION public.prune_audit_log_by_plan(int) FROM PUBLIC;
GRANT  EXECUTE ON FUNCTION public.prune_audit_log_by_plan(int) TO eurobase_gateway;

COMMIT;
