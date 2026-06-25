-- 000066_data_access_log.up.sql
--
-- Tier-1 GDPR #4 (Art. 30 records of processing / Art. 32 — every access to
-- personal data is logged). public.audit_log records sensitive *admin/platform*
-- actions; it does NOT record who *read* tenant personal data over the SDK.
-- This migration adds public.data_access_log to close that gap.
--
-- Volume & shape
-- ==============
--   * One row per personal-data read / export / download. Far higher volume
--     than audit_log, so it is range-partitioned by month — old months can be
--     detached/dropped cheaply by the retention job (#171) without a giant
--     DELETE, and queries that filter on created_at prune to a few partitions.
--   * Written by the gateway via an async, sampled, batched recorder
--     (internal/audit/access.go) — never on the request's critical path.
--
-- NOT a hash chain
-- ================
-- Unlike audit_log (000058), this table is intentionally NOT a per-row hash
-- chain: it is high-volume and sampled, and the async batched writer cannot
-- hold a per-project advisory lock per row without becoming a bottleneck.
-- Tamper-evidence for this stream is provided by the off-box WORM dump
-- (#170/#171). The in-DB protection here is append-only (REVOKE UPDATE/DELETE
-- from the runtime roles), matching audit_log.
--
-- No explicit BEGIN/COMMIT: golang-migrate wraps each .up.sql in its own tx.

-- ── 1. Partitioned parent ──────────────────────────────────────────────
-- The partition key (created_at) must be part of any unique constraint, so
-- the primary key is (id, created_at) rather than id alone.
CREATE TABLE public.data_access_log (
    id           UUID        NOT NULL DEFAULT uuid_generate_v4(),
    project_id   UUID        REFERENCES public.projects(id) ON DELETE SET NULL,
    end_user_id  UUID,                                   -- the data subject / caller; NULL for service/anon
    actor_role   TEXT        NOT NULL,                   -- 'authenticated' | 'service' | 'anon' | 'platform'
    action       TEXT        NOT NULL,                   -- 'read' | 'export' | 'download'
    target_table TEXT        NOT NULL,                   -- tenant table or 'storage_objects'
    target_keys  JSONB       NOT NULL DEFAULT '{}'::jsonb, -- {"id": "..."} / {"filters": [...]} / {"key": "..."}
    ip           TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

COMMENT ON TABLE public.data_access_log IS
  'Tier-1 GDPR #4: per-access log of personal-data reads/exports/downloads. Append-only for runtime roles; monthly partitions; retention via #171.';

-- Query patterns: "what did this user/project access, recently?" — both lead
-- with created_at DESC so the planner can prune partitions then index-scan.
CREATE INDEX idx_data_access_log_project ON public.data_access_log (project_id, created_at DESC);
CREATE INDEX idx_data_access_log_user    ON public.data_access_log (end_user_id, created_at DESC);
CREATE INDEX idx_data_access_log_action  ON public.data_access_log (action, created_at DESC);

-- ── 2. Partition helper ────────────────────────────────────────────────
-- Creates the monthly partition covering p_month (truncated to the 1st) if it
-- does not already exist. Idempotent. Used by the pre-create loop below and by
-- the rolling-creation job in #171. SECURITY DEFINER so a future scheduled
-- caller without DDL rights can still roll partitions forward; owned by the
-- migrator (the public.* owner) per CLAUDE.md.
CREATE OR REPLACE FUNCTION public.ensure_data_access_log_partition(p_month date)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_start date := date_trunc('month', p_month)::date;
    v_end   date := (date_trunc('month', p_month) + interval '1 month')::date;
    v_name  text := format('data_access_log_%s', to_char(v_start, 'YYYYMM'));
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_class WHERE relname = v_name) THEN
        EXECUTE format(
            'CREATE TABLE public.%I PARTITION OF public.data_access_log FOR VALUES FROM (%L) TO (%L)',
            v_name, v_start, v_end);
        -- 000037 auto-grants gateway full DML on every migrator-created table.
        -- Strip UPDATE/DELETE so a partition addressed directly is append-only
        -- too, not just via the parent. Done here so EVERY partition (the
        -- pre-create loop below and the rolling job in #171) is consistent.
        EXECUTE format('REVOKE UPDATE, DELETE ON public.%I FROM eurobase_gateway', v_name);
        IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'eurobase_developer') THEN
            EXECUTE format('REVOKE UPDATE, DELETE, INSERT ON public.%I FROM eurobase_developer', v_name);
        END IF;
    END IF;
END$$;

ALTER FUNCTION public.ensure_data_access_log_partition(date) OWNER TO eurobase_migrator;

-- ── 3. Pre-create near-term partitions + a catch-all default ───────────
-- Pre-create the current month and the next 11 so writes have a home for a
-- year even if the rolling job (#171) is not yet deployed. The DEFAULT
-- partition guarantees inserts NEVER fail on a missing month (rows simply land
-- in default and can be redistributed later).
DO $$
DECLARE
    m date := date_trunc('month', now())::date;
    i int;
BEGIN
    FOR i IN 0..11 LOOP
        PERFORM public.ensure_data_access_log_partition((m + (i || ' month')::interval)::date);
    END LOOP;
END$$;

CREATE TABLE public.data_access_log_default
    PARTITION OF public.data_access_log DEFAULT;

-- ── 4. Grants ──────────────────────────────────────────────────────────
-- The recorder connects as eurobase_gateway. It only ever INSERTs; SELECT is
-- granted so the (future) compliance console can read the stream through the
-- gateway pool. Grants on the parent cascade to existing and future
-- partitions.
GRANT INSERT, SELECT ON public.data_access_log TO eurobase_gateway;
GRANT EXECUTE ON FUNCTION public.ensure_data_access_log_partition(date) TO eurobase_gateway;

-- ── 5. Append-only for the runtime roles ───────────────────────────────
-- Defence-in-depth mirroring audit_log (000058): the runtime gateway can add
-- rows but never rewrite or erase history. Only eurobase_migrator (deploy-only,
-- table owner) keeps UPDATE/DELETE — needed by the retention job to drop old
-- partitions. The 000037 blanket grant handed gateway full DML on public.*, so
-- REVOKE the parts we don't want here.
-- Parent + DEFAULT partition (the pre-created monthly partitions are already
-- locked down inside ensure_data_access_log_partition above).
REVOKE UPDATE, DELETE ON public.data_access_log         FROM eurobase_gateway;
REVOKE UPDATE, DELETE ON public.data_access_log_default FROM eurobase_gateway;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'eurobase_developer') THEN
        EXECUTE 'GRANT  SELECT                ON public.data_access_log         TO   eurobase_developer';
        EXECUTE 'REVOKE UPDATE, DELETE, INSERT ON public.data_access_log         FROM eurobase_developer';
        EXECUTE 'REVOKE UPDATE, DELETE, INSERT ON public.data_access_log_default FROM eurobase_developer';
    END IF;
END$$;
