-- 000097_audit_log_archive.up.sql
--
-- Bridge to #170 (off-box WORM dump). The surrounding retention code
-- has always documented "pruning is safe because the off-box WORM
-- dump keeps a durable copy" — but #170 was never implemented, and
-- #317's per-plan pruning makes deletion active-by-default for Team
-- and Legal-Team projects. Without a durable copy the deploy would
-- destroy audit rows the surrounding code promised were retained
-- (and, worse, an upgrade from Free → Team would silently ratchet
-- retention *down* on the next tick — accumulated Free-era history
-- past the Team cap would be permanently gone).
--
-- This table is the stopgap: prune_audit_log_by_plan copies each
-- to-be-deleted row here before removing it from audit_log. #170
-- becomes "dump this table off-box then TRUNCATE" — same shape, no
-- data lost in the meantime, and the CI/verifier surface doesn't
-- need to know about the archive.
--
-- Design notes:
--   * Same columns as audit_log so a SELECT * INSERT works with no
--     column list drift over time (each ADD COLUMN on audit_log
--     needs a matching ADD COLUMN here — enforced by a test in
--     retention_test.go that diffs pg_catalog).
--   * archived_at + archived_reason for operator visibility.
--     archived_reason carries the plan code that triggered the
--     prune, so `SELECT count(*) GROUP BY archived_reason` gives
--     ops the same per-tier telemetry the worker log line does.
--   * No FKs — archive keeps history even after the referenced
--     project / actor is deleted.
--   * No PK on id (audit_log's id is UUID; two archive-runs would
--     conflict if we UPSERTed, and we deliberately never re-archive
--     the same row: the prune loop DELETEs immediately after
--     inserting, so uniqueness is a natural invariant). A composite
--     unique on (id, archived_at) would let a mis-configured
--     re-run coexist safely if the archive ever grew a second
--     writer — cheap insurance.
--   * REVOKE FROM PUBLIC per the #217 convention. eurobase_gateway
--     needs INSERT (via the SECURITY DEFINER prune function, which
--     runs as migrator anyway — but the grant kept for symmetry with
--     audit_log's grants).

BEGIN;

CREATE TABLE public.audit_log_archive (
    id              UUID        NOT NULL,
    project_id      UUID,
    actor_id        UUID,
    actor_email     TEXT        NOT NULL,
    action          TEXT        NOT NULL,
    target_type     TEXT,
    target_id       TEXT,
    metadata        JSONB       DEFAULT '{}'::jsonb,
    ip_address      TEXT,
    created_at      TIMESTAMPTZ NOT NULL,

    -- hash-chain columns preserved so a future off-box dump can
    -- carry the chain forward as-is (Verify semantics are per-
    -- project, so archive doesn't need to be verifiable on its own;
    -- it just needs to hand the columns back to whoever exports
    -- them).
    seq             BIGINT      NOT NULL,
    prev_hash       BYTEA,
    row_hash        BYTEA       NOT NULL,

    archived_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    archived_reason TEXT        NOT NULL,

    UNIQUE (id, archived_at)
);

CREATE INDEX ix_audit_log_archive_project    ON public.audit_log_archive(project_id, created_at DESC);
CREATE INDEX ix_audit_log_archive_archived_at ON public.audit_log_archive(archived_at);

GRANT SELECT, INSERT ON public.audit_log_archive TO eurobase_gateway;
GRANT SELECT, INSERT, DELETE, TRUNCATE ON public.audit_log_archive TO eurobase_developer;

-- Now update prune_audit_log_by_plan to write the archive row
-- BEFORE the delete. CREATE OR REPLACE preserves the function's
-- OID + grants, so no re-GRANT dance.

CREATE OR REPLACE FUNCTION public.prune_audit_log_by_plan(fallback_days int)
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

        -- Copy the to-be-deleted rows to the archive BEFORE the
        -- delete. Same predicate the DELETE below uses. The archive
        -- carries archived_reason = the plan code so ops can slice
        -- the archive by tier.
        INSERT INTO public.audit_log_archive
            (id, project_id, actor_id, actor_email, action,
             target_type, target_id, metadata, ip_address,
             created_at, seq, prev_hash, row_hash,
             archived_reason)
        SELECT
            id, project_id, actor_id, actor_email, action,
            target_type, target_id, metadata, ip_address,
            created_at, seq, prev_hash, row_hash,
            r.plan_code
          FROM public.audit_log
         WHERE project_id IS NOT DISTINCT FROM r.project_id
           AND seq <= v_last_seq;

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

        o_plan         := r.plan_code;
        o_project_id   := r.project_id;
        o_rows_deleted := v_deleted;
        RETURN NEXT;
    END LOOP;
    RETURN;
END$$;

COMMIT;
