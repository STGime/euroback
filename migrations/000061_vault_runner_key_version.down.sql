-- Restores the 000049 two-column signature. WARNING: an updated runner
-- (selecting key_version) will fail against this version — only roll
-- back together with the pre-#201 runner image. Reverting also re-breaks
-- decryption of key_version >= 1 secrets from edge functions (the bug
-- this migration fixes).

BEGIN;

DROP FUNCTION IF EXISTS public.vault_get_for_runner(uuid, text);

CREATE FUNCTION public.vault_get_for_runner(
    p_project_id uuid,
    p_name       text
)
RETURNS TABLE(encrypted bytea, nonce bytea)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
DECLARE
    v_schema text;
BEGIN
    SELECT schema_name INTO v_schema
    FROM public.projects
    WHERE id = p_project_id;

    IF v_schema IS NULL THEN
        RETURN;
    END IF;

    RETURN QUERY EXECUTE format(
        'SELECT secret, nonce FROM %I.vault_secrets WHERE name = $1',
        v_schema
    ) USING p_name;
END;
$$;

ALTER FUNCTION public.vault_get_for_runner(uuid, text) OWNER TO eurobase_migrator;
REVOKE ALL ON FUNCTION public.vault_get_for_runner(uuid, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.vault_get_for_runner(uuid, text) TO eurobase_function_runner;

COMMIT;
