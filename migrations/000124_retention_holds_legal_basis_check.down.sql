-- Reverse of 000124. The constraint name is the same one 000089 creates on
-- fresh databases, so on those this also removes 089's CHECK — matching
-- prod as it was before 000124.
ALTER TABLE public.retention_holds DROP CONSTRAINT IF EXISTS retention_holds_legal_basis_check;
