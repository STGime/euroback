-- 000058_audit_hash_chain.up.sql
--
-- Tier-1 GDPR #3 (immutable audit logging — Art. 5(2), 30, 32). Makes
-- public.audit_log tamper-EVIDENT via a per-project hash chain, and
-- tamper-RESISTANT by revoking UPDATE/DELETE from the runtime roles.
--
-- Model
-- =====
--   * Each row stores row_hash = SHA-256(prev_hash || canonical(row)), where
--     prev_hash is the row_hash of the previous row in the SAME project's
--     chain (NULL for the first row). Altering, deleting, reordering, or
--     inserting a row breaks the chain and is detected by Verify().
--   * `seq` (a dedicated monotonic sequence) gives the chain a total order
--     that is robust to created_at collisions under concurrent writes.
--   * The hash is computed by an IMMUTABLE function so INSERT and Verify use
--     byte-identical logic. created_at is rendered at UTC so the hash does
--     not depend on the session timezone.
--
-- Tamper resistance: runtime roles (gateway, developer) lose UPDATE/DELETE —
-- they may only INSERT/SELECT. Only eurobase_migrator (deploy-only) retains
-- full rights, by necessity. The independent off-box WORM copy (signed
-- object-store dump) ships in the SIEM-export / retention PRs and is the
-- defence against an owner-level tamper.
--
-- No explicit BEGIN/COMMIT: golang-migrate wraps each .up.sql in its own tx.

-- ── 1. Canonical row-hash function (shared by INSERT and Verify) ───────
CREATE OR REPLACE FUNCTION public.audit_row_hash(
    p_prev        bytea,
    p_project_id  uuid,
    p_actor_id    uuid,
    p_actor_email text,
    p_action      text,
    p_target_type text,
    p_target_id   text,
    p_metadata    jsonb,
    p_ip_address  text,
    p_created_at  timestamptz
) RETURNS bytea
LANGUAGE sql IMMUTABLE AS $$
    SELECT sha256(
        COALESCE(p_prev, ''::bytea) ||
        convert_to(
            COALESCE(p_project_id::text, '')                                              || E'\x1e' ||
            COALESCE(p_actor_id::text, '')                                                || E'\x1e' ||
            COALESCE(p_actor_email, '')                                                   || E'\x1e' ||
            COALESCE(p_action, '')                                                        || E'\x1e' ||
            COALESCE(p_target_type, '')                                                   || E'\x1e' ||
            COALESCE(p_target_id, '')                                                     || E'\x1e' ||
            COALESCE(p_metadata::text, '{}')                                              || E'\x1e' ||
            COALESCE(p_ip_address, '')                                                    || E'\x1e' ||
            -- UTC + explicit format so the hash is timezone-independent.
            COALESCE(to_char(p_created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US'), ''),
        'UTF8')
    )
$$;

ALTER FUNCTION public.audit_row_hash(bytea,uuid,uuid,text,text,text,text,jsonb,text,timestamptz)
    OWNER TO eurobase_migrator;
GRANT EXECUTE ON FUNCTION public.audit_row_hash(bytea,uuid,uuid,text,text,text,text,jsonb,text,timestamptz)
    TO eurobase_gateway;

-- ── 2. New columns ─────────────────────────────────────────────────────
ALTER TABLE public.audit_log ADD COLUMN seq       bigint;
ALTER TABLE public.audit_log ADD COLUMN prev_hash bytea;
ALTER TABLE public.audit_log ADD COLUMN row_hash  bytea;

-- ── 3. Assign a total order to existing rows, then attach the sequence ─
WITH ordered AS (
    SELECT id, row_number() OVER (ORDER BY created_at, id) AS rn
    FROM public.audit_log
)
UPDATE public.audit_log a SET seq = o.rn FROM ordered o WHERE a.id = o.id;

CREATE SEQUENCE public.audit_log_seq OWNED BY public.audit_log.seq;
SELECT setval('public.audit_log_seq', COALESCE((SELECT max(seq) FROM public.audit_log), 0) + 1, false);
ALTER TABLE public.audit_log ALTER COLUMN seq SET DEFAULT nextval('public.audit_log_seq');
ALTER TABLE public.audit_log ALTER COLUMN seq SET NOT NULL;
CREATE UNIQUE INDEX idx_audit_log_seq ON public.audit_log(seq);
-- gateway inserts rows, so it needs the sequence.
GRANT USAGE, SELECT ON SEQUENCE public.audit_log_seq TO eurobase_gateway;

-- ── 4. Backfill the hash chain in seq order, per project ───────────────
DO $$
DECLARE
    r       RECORD;
    v_prev  bytea := NULL;
    v_proj  uuid;
    v_first boolean := true;
    v_hash  bytea;
BEGIN
    FOR r IN SELECT * FROM public.audit_log ORDER BY seq ASC LOOP
        -- New chain whenever the project changes (NULL-project rows form
        -- their own chain, matching IS NOT DISTINCT FROM semantics).
        IF v_first OR r.project_id IS DISTINCT FROM v_proj THEN
            v_prev  := NULL;
            v_proj  := r.project_id;
            v_first := false;
        END IF;
        v_hash := public.audit_row_hash(v_prev, r.project_id, r.actor_id, r.actor_email,
                                        r.action, r.target_type, r.target_id, r.metadata,
                                        r.ip_address, r.created_at);
        UPDATE public.audit_log SET prev_hash = v_prev, row_hash = v_hash WHERE id = r.id;
        v_prev := v_hash;
    END LOOP;
END$$;

ALTER TABLE public.audit_log ALTER COLUMN row_hash SET NOT NULL;

-- ── 5. Make it append-only for the runtime roles ──────────────────────
REVOKE UPDATE, DELETE ON public.audit_log FROM eurobase_gateway;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'eurobase_developer') THEN
        EXECUTE 'REVOKE UPDATE, DELETE ON public.audit_log FROM eurobase_developer';
    END IF;
END$$;
