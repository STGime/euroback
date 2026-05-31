-- 000058_audit_hash_chain.down.sql
--
-- Reverts the audit_log hash chain. Restores UPDATE/DELETE to the runtime
-- roles and drops the chain columns + sequence + hash function.
--
-- WARNING: this removes the tamper-evidence guarantee. Only the off-box
-- WORM dump (if configured) remains as integrity evidence afterwards.

-- Restore runtime write access first.
GRANT UPDATE, DELETE ON public.audit_log TO eurobase_gateway;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'eurobase_developer') THEN
        EXECUTE 'GRANT UPDATE, DELETE ON public.audit_log TO eurobase_developer';
    END IF;
END$$;

DROP INDEX IF EXISTS public.idx_audit_log_seq;
ALTER TABLE public.audit_log ALTER COLUMN seq DROP DEFAULT;
ALTER TABLE public.audit_log DROP COLUMN IF EXISTS row_hash;
ALTER TABLE public.audit_log DROP COLUMN IF EXISTS prev_hash;
ALTER TABLE public.audit_log DROP COLUMN IF EXISTS seq;
DROP SEQUENCE IF EXISTS public.audit_log_seq;

DROP FUNCTION IF EXISTS public.audit_row_hash(bytea,uuid,uuid,text,text,text,text,jsonb,text,timestamptz);
