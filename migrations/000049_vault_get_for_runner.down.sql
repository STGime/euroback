-- 000049_vault_get_for_runner.down.sql
--
-- Reverse 000049: drop the SECURITY DEFINER vault read helper. After this
-- runs, ctx.vault.get(name) inside edge functions returns null again.

BEGIN;

DROP FUNCTION IF EXISTS public.vault_get_for_runner(uuid, text);

COMMIT;
