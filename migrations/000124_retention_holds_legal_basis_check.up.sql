-- 000124_retention_holds_legal_basis_check.up.sql
--
-- #632: bring prod's retention_holds in line with 000089's intended shape.
-- 000089 declares `legal_basis TEXT NOT NULL CHECK (length(legal_basis) > 0)`,
-- but prod never ran 000089 — its table came from the 000094 recovery
-- migration, which dropped the CHECK. Fresh databases (built from 089)
-- have it, prod doesn't; this closes that drift.
--
-- A legal hold without a stated legal basis (§50 BRAO / §257 HGB /
-- §147 AO …) is meaningless, and the API already rejects an empty value
-- (internal/compliance/retention_holds.go). Added NOT VALID first so the
-- deploy can't fail on unexpected legacy rows, then validated; if
-- validation fails the constraint stays NOT VALID (still enforced for new
-- and updated rows) and a NOTICE says so.
--
-- Named like PostgreSQL's auto-generated name in 000089, so the
-- existence check is a no-op on fresh databases.

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
         WHERE conrelid = 'public.retention_holds'::regclass
           AND conname  = 'retention_holds_legal_basis_check'
    ) THEN
        ALTER TABLE public.retention_holds
            ADD CONSTRAINT retention_holds_legal_basis_check
            CHECK (length(legal_basis) > 0) NOT VALID;
        BEGIN
            ALTER TABLE public.retention_holds
                VALIDATE CONSTRAINT retention_holds_legal_basis_check;
        EXCEPTION WHEN check_violation THEN
            RAISE NOTICE 'retention_holds has rows with an empty legal_basis; constraint left NOT VALID (enforced for new rows only)';
        END;
    END IF;
END$$;
