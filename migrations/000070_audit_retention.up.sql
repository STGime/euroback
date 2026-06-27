-- 000070_audit_retention.up.sql
--
-- Tier-1 GDPR #3 follow-up (#171): retention worker for the two append-only
-- audit streams.
--
-- What this migration adds
-- ========================
--   1. `public.audit_log_chain_checkpoints` — per-project bookmark recording
--      the last pruned `seq` + `row_hash`. Lets `Verify()` start from a
--      non-genesis prev_hash so pruning the oldest rows does NOT show up as a
--      chain break.
--   2. `public.prune_audit_log(retention_days)` — SECURITY DEFINER helper
--      owned by the migrator that DELETEs audit_log rows older than the
--      retention horizon, per project, and UPSERTs the checkpoint. Returns
--      total rows deleted. Gateway has EXECUTE so the worker (which connects
--      as `eurobase_gateway`) can call it; runtime roles still lack direct
--      DELETE on `public.audit_log` (000058).
--   3. `public.drop_old_data_access_log_partitions(retention_months)` —
--      detaches + drops monthly partitions whose end < cutoff. Returns the
--      list of dropped partition names (for log lines / observability).
--   4. `public.ensure_future_data_access_log_partitions(months_ahead)` —
--      idempotent rolling pre-create. Loops over current month + N months
--      ahead and calls the existing `ensure_data_access_log_partition`
--      helper so writes never land in `DEFAULT`.
--
-- Tamper-evidence and pruning
-- ===========================
-- The in-DB hash chain (000058) catches modification, deletion, and
-- reordering of *existing* rows. Pruning the OLDEST rows for retention
-- inevitably "deletes" rows — naïvely, that would break `Verify`'s linkage
-- check at the first surviving row (its `prev_hash` no longer matches the
-- nil starting state). The checkpoint table fixes this: when Verify starts
-- a chain it loads `last_pruned_row_hash` as its initial `prev`. The chain
-- is still cryptographically continuous, just with a recorded prefix that
-- lives in the off-box WORM dump (#170).
--
-- The chain head (most recent row of each project) is never pruned, so
-- ongoing appends keep linking against fresh prev_hashes — they don't even
-- know pruning happened.
--
-- Default retention
-- =================
-- Both helpers take retention as a parameter; defaults live in the Go
-- caller. The shipped defaults (see `internal/workers/audit_retention.go`)
-- are:
--   * audit_log:        0 days  → "never prune in DB" (off-box WORM is the
--                                 long-term store). Operators can flip to a
--                                 positive number to enable in-DB pruning.
--   * data_access_log: 395 days → ~13 months of monthly partitions kept hot,
--                                 1 buffer month so the rolling pre-create
--                                 has slack. The signed object dump in #170
--                                 retains beyond this.
--
-- Convention (CLAUDE.md): every new SECURITY DEFINER function in `public`
-- REVOKEs EXECUTE from PUBLIC and GRANTs only to the runtime role that
-- needs it. Enforced manually here per function.
--
-- No explicit BEGIN/COMMIT: golang-migrate wraps each .up.sql in its own tx.

-- ── 1. Per-project chain checkpoint ────────────────────────────────────
-- Stores the (seq, row_hash) of the last row PRUNED from each project's
-- chain. Verify() seeds its initial `prev` from `last_row_hash` when a
-- checkpoint exists, so the first surviving row's `prev_hash` still links
-- correctly. NULL-project rows share the `__global__` chain in the writer;
-- their checkpoint key is the all-zero UUID by convention so the table can
-- have a UUID PK without nullable column gymnastics.
CREATE TABLE public.audit_log_chain_checkpoints (
    project_id      uuid PRIMARY KEY,
    last_pruned_seq bigint      NOT NULL,
    last_row_hash   bytea       NOT NULL,
    updated_at      timestamptz NOT NULL DEFAULT now()
);

COMMENT ON TABLE public.audit_log_chain_checkpoints IS
  'Tier-1 GDPR #3 retention bookmark (#171): per-project last-pruned (seq, row_hash). Used by audit.Verify to start from a non-genesis prev_hash after retention pruning.';

GRANT SELECT ON public.audit_log_chain_checkpoints TO eurobase_gateway;

-- ── 2. audit_log prune helper ──────────────────────────────────────────
-- Deletes rows older than `cutoff_days` per project, capturing the (seq,
-- row_hash) of the newest row that was deleted into the checkpoint table.
-- The chain head (newest row) is preserved by definition — we never
-- delete rows newer than cutoff. Per-project advisory lock prevents
-- racing with audit appends on the same project (Service.Log uses
-- `pg_advisory_xact_lock(hashtext(project_id))` on the same key shape).
--
-- Returns total rows deleted across all projects.
--
-- Multi-pod safety. Two worker pods that enter this function at the same
-- time DO race on the head-snapshot SELECT below — but the per-chain
-- advisory lock taken inside the loop body serializes them, AND we re-do
-- the "find newest prunable row" probe under the lock, so pod 2 just sees
-- v_last_seq IS NULL after pod 1 finished and CONTINUEs. Safe today and
-- under HPA. The cross-project `drop_old_data_access_log_partitions` is
-- not similarly safe — see its header comment.
CREATE OR REPLACE FUNCTION public.prune_audit_log(cutoff_days int)
RETURNS bigint
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_cutoff   timestamptz;
    v_total    bigint := 0;
    v_deleted  bigint;
    r          RECORD;
    v_last_seq bigint;
    v_last_h   bytea;
    v_chainkey text;
BEGIN
    IF cutoff_days IS NULL OR cutoff_days <= 0 THEN
        RETURN 0;
    END IF;
    v_cutoff := now() - make_interval(days => cutoff_days);

    -- Group by project (NULL → __global__) and prune each chain
    -- independently. Order matters: we always preserve the chain HEAD
    -- (newest seq) of each project, and only ever delete rows with
    -- seq < the head's seq. A chain whose head itself is older than
    -- cutoff is left alone — the chain never gets pruned entirely.
    --
    -- NOTE on the head_seq subselect: r.head_seq is sampled BEFORE the
    -- advisory lock is acquired, so it may be stale by the time the loop
    -- body runs (a writer could have appended a newer head). That is
    -- safe: a stale (older) head_seq only causes us to UNDER-prune
    -- (`seq < stale_head` excludes more rows than `seq < real_head`),
    -- never to delete the actual current head. Do NOT hoist the lock to
    -- cover the outer SELECT — that would serialize prune across all
    -- chains and is unnecessary.
    FOR r IN
        SELECT
            COALESCE(project_id::text, '__global__') AS chain_key,
            project_id,
            (SELECT max(seq) FROM public.audit_log
             WHERE project_id IS NOT DISTINCT FROM a.project_id) AS head_seq
        FROM public.audit_log a
        GROUP BY project_id
    LOOP
        v_chainkey := r.chain_key;
        -- Serialize against concurrent appends on the same chain AND
        -- against another worker pod also pruning the same chain.
        PERFORM pg_advisory_xact_lock(hashtext(v_chainkey));

        -- Find the newest row we're about to delete (highest seq < head
        -- with created_at < cutoff). That row becomes the checkpoint.
        SELECT seq, row_hash
          INTO v_last_seq, v_last_h
          FROM public.audit_log
         WHERE project_id IS NOT DISTINCT FROM r.project_id
           AND created_at < v_cutoff
           AND seq < r.head_seq
         ORDER BY seq DESC
         LIMIT 1;

        IF v_last_seq IS NULL THEN
            -- Nothing prunable on this project.
            CONTINUE;
        END IF;

        DELETE FROM public.audit_log
         WHERE project_id IS NOT DISTINCT FROM r.project_id
           AND seq <= v_last_seq;
        GET DIAGNOSTICS v_deleted = ROW_COUNT;
        v_total := v_total + v_deleted;

        -- UPSERT checkpoint. Key uses NULL UUID for the global chain.
        INSERT INTO public.audit_log_chain_checkpoints
            (project_id, last_pruned_seq, last_row_hash, updated_at)
        VALUES
            (COALESCE(r.project_id, '00000000-0000-0000-0000-000000000000'::uuid),
             v_last_seq, v_last_h, now())
        ON CONFLICT (project_id) DO UPDATE
           SET last_pruned_seq = EXCLUDED.last_pruned_seq,
               last_row_hash   = EXCLUDED.last_row_hash,
               updated_at      = EXCLUDED.updated_at;
    END LOOP;

    RETURN v_total;
END$$;

ALTER FUNCTION public.prune_audit_log(int) OWNER TO eurobase_migrator;
REVOKE EXECUTE ON FUNCTION public.prune_audit_log(int) FROM PUBLIC;
GRANT  EXECUTE ON FUNCTION public.prune_audit_log(int) TO eurobase_gateway;

-- ── 3. data_access_log partition drop helper ──────────────────────────
-- Detaches and DROPs monthly partitions whose UPPER bound is on or before
-- the retention cutoff. Returns the list of dropped partition names so the
-- worker can log them. Never touches DEFAULT or the current month.
--
-- pg_class.relname holds the partition name; pg_get_expr decodes the
-- bound expression. We don't parse pg_get_expr — instead we go through
-- pg_inherits + pg_partition_tree to read the actual bound via
-- pg_get_partition_constraintdef, but that's expensive on PG14+. Simpler:
-- the helper `ensure_data_access_log_partition` names partitions
-- `data_access_log_YYYYMM`; we walk pg_class for that pattern and parse
-- the month out of the name. That's deterministic since every partition
-- we create goes through that helper.
--
-- Multi-pod safety. Without coordination, two pods could enumerate
-- pg_inherits, both attempt DETACH on the same partition name, and the
-- loser's error aborts the whole function (the per-iteration EXECUTE has
-- no savepoint, so subsequent drops in the same call are skipped and
-- v_dropped is lost). We acquire a single advisory xact lock at function
-- entry to serialize cross-pod calls — pod 2 blocks behind pod 1, runs
-- against post-drop state, finds nothing to drop, returns empty. The
-- lock is held for the duration of the tx (the function's caller commit),
-- which is fine because retention is a daily housekeeping pass — there
-- is no hot path waiting on this.
--
-- Mis-named partitions (operator created `data_access_log_2026_q1` etc.)
-- don't match the regex and are silently skipped. Surface that as a
-- WARNING so an operator notices in logs rather than discovering at the
-- 13-month horizon that nothing was ever pruned.
CREATE OR REPLACE FUNCTION public.drop_old_data_access_log_partitions(cutoff_months int)
RETURNS text[]
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_cutoff_month  date;
    v_dropped       text[] := ARRAY[]::text[];
    r               RECORD;
    v_part_month    date;
    v_unrecognized  int := 0;
BEGIN
    IF cutoff_months IS NULL OR cutoff_months <= 0 THEN
        RETURN v_dropped;
    END IF;

    -- Cross-pod serialization (see header). hashtext() of a fixed string
    -- gives a stable int4 lock key.
    PERFORM pg_advisory_xact_lock(hashtext('data_access_log_retention'));

    -- Drop partitions whose covered month ends on or before the cutoff.
    -- Example: cutoff_months=13 + today=2027-03-15 → cutoff_month=2026-02-01;
    -- partitions for 2026-01 or earlier are dropped.
    v_cutoff_month := (date_trunc('month', now()) - make_interval(months => cutoff_months))::date;

    -- Count partitions that DON'T match the YYYYMM convention so an
    -- operator notices manual partitions accumulating outside the
    -- retention sweep.
    SELECT count(*) INTO v_unrecognized
    FROM   pg_inherits i
    JOIN   pg_class c    ON c.oid = i.inhrelid
    JOIN   pg_class pc   ON pc.oid = i.inhparent
    WHERE  pc.relname = 'data_access_log'
      AND  c.relname <> 'data_access_log_default'
      AND  c.relname !~ '^data_access_log_[0-9]{6}$';
    IF v_unrecognized > 0 THEN
        RAISE WARNING 'data_access_log retention: % partition(s) skipped — name not in data_access_log_YYYYMM form', v_unrecognized;
    END IF;

    FOR r IN
        SELECT c.relname AS name
        FROM   pg_inherits i
        JOIN   pg_class c    ON c.oid = i.inhrelid
        JOIN   pg_class pc   ON pc.oid = i.inhparent
        WHERE  pc.relname = 'data_access_log'
          AND  c.relname  ~ '^data_access_log_[0-9]{6}$'
        ORDER BY c.relname
    LOOP
        -- Name format: data_access_log_YYYYMM
        v_part_month := to_date(substring(r.name from 17 for 6), 'YYYYMM');
        IF v_part_month <= v_cutoff_month THEN
            EXECUTE format('ALTER TABLE public.data_access_log DETACH PARTITION public.%I', r.name);
            EXECUTE format('DROP TABLE public.%I', r.name);
            v_dropped := array_append(v_dropped, r.name);
        END IF;
    END LOOP;

    RETURN v_dropped;
END$$;

ALTER FUNCTION public.drop_old_data_access_log_partitions(int) OWNER TO eurobase_migrator;
REVOKE EXECUTE ON FUNCTION public.drop_old_data_access_log_partitions(int) FROM PUBLIC;
GRANT  EXECUTE ON FUNCTION public.drop_old_data_access_log_partitions(int) TO eurobase_gateway;

-- ── 4. data_access_log future-partition pre-create ────────────────────
-- Idempotent rolling pre-create. The migration 000066 pre-creates the
-- current month + 11; this helper extends that horizon on every worker
-- tick so the rolling window never closes. months_ahead=12 keeps a
-- year of forward partitions ready at all times.
CREATE OR REPLACE FUNCTION public.ensure_future_data_access_log_partitions(months_ahead int)
RETURNS int
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    m date;
    i int;
    n int := 0;
BEGIN
    IF months_ahead IS NULL OR months_ahead < 0 THEN
        RETURN 0;
    END IF;
    m := date_trunc('month', now())::date;
    FOR i IN 0..months_ahead LOOP
        PERFORM public.ensure_data_access_log_partition((m + make_interval(months => i))::date);
        n := n + 1;
    END LOOP;
    RETURN n;
END$$;

ALTER FUNCTION public.ensure_future_data_access_log_partitions(int) OWNER TO eurobase_migrator;
REVOKE EXECUTE ON FUNCTION public.ensure_future_data_access_log_partitions(int) FROM PUBLIC;
GRANT  EXECUTE ON FUNCTION public.ensure_future_data_access_log_partitions(int) TO eurobase_gateway;
