BEGIN;

-- DROP + CREATE (not CREATE OR REPLACE): PG rejects any change to
-- the RETURNS TABLE column names via OR REPLACE. This down path
-- restores the archive-less prune body — but a symmetric up amend
-- may or may not have renamed the output params, so DROP-first
-- keeps the migration idempotent regardless of the target DB's
-- current signature.
DROP FUNCTION IF EXISTS public.prune_audit_log_by_plan(int);

CREATE FUNCTION public.prune_audit_log_by_plan(fallback_days int)
RETURNS TABLE(o_plan text, o_project_id uuid, o_rows_deleted bigint)
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
        SELECT seq, row_hash INTO v_last_seq, v_last_h
          FROM public.audit_log
         WHERE project_id IS NOT DISTINCT FROM r.project_id
           AND created_at < v_cutoff AND seq < r.head_seq
         ORDER BY seq DESC LIMIT 1;
        IF v_last_seq IS NULL THEN CONTINUE; END IF;
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
        o_plan := r.plan_code; o_project_id := r.project_id; o_rows_deleted := v_deleted;
        RETURN NEXT;
    END LOOP;
    RETURN;
END$$;

-- Re-apply the #217 lockdown ACLs after DROP.
ALTER FUNCTION public.prune_audit_log_by_plan(int) OWNER TO eurobase_migrator;
REVOKE EXECUTE ON FUNCTION public.prune_audit_log_by_plan(int) FROM PUBLIC;
GRANT  EXECUTE ON FUNCTION public.prune_audit_log_by_plan(int) TO eurobase_gateway;

DROP TABLE IF EXISTS public.audit_log_archive;

COMMIT;
