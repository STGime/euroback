-- Closes #120. Universal DDL audit via Postgres event triggers.
--
-- Today only DDL that flows through the platform's typed handlers
-- (HandleCreateTable, HandleAddColumn, …) lands in schema_changes.
-- Everything else — console SQL runner, MCP runSQL, SDK runSQL, direct
-- DB access — silently bypasses the audit. Migration History is a
-- partial ledger.
--
-- This migration installs two event triggers that fire on every DDL
-- command regardless of the path that issued it:
--
--   * trg_log_ddl_end   ON ddl_command_end  — CREATE / ALTER
--   * trg_log_ddl_drop  ON sql_drop         — DROP (at sql_drop time
--                                              the dropped object is
--                                              already gone from the
--                                              catalog, so we can't
--                                              learn about it later)
--
-- Filtering — only tenant schemas (`tenant_*`) generate audit rows.
-- The platform's own DDL (public.*, anything the migrator owns at the
-- top level) is skipped to keep schema_changes per-tenant.
--
-- The trigger functions are SECURITY DEFINER and owned by
-- eurobase_migrator, so any role with DDL privileges on a tenant
-- schema (gateway via SDK DDL, developer pool via console, etc.) can
-- write through them without needing INSERT on public.schema_changes.
--
-- Resilience — every log path is wrapped in EXCEPTION blocks. A bug
-- in the trigger function MUST NOT cause the user's DDL to roll back.

BEGIN;

-- ── ddl_command_end: CREATE / ALTER ────────────────────────────────
--
-- pg_event_trigger_ddl_commands() returns one row per DDL command in
-- the statement. command_tag tells us what happened at the SQL level;
-- object_type narrows it for ALTER TABLE (one row per affected sub-
-- object). We map only the cases that match a known
-- schema_changes.action enum value; unmappable rows are skipped (the
-- typed handlers' logSchemaChange covers the rest, when it's used).
CREATE OR REPLACE FUNCTION public.log_ddl_event()
RETURNS event_trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_catalog
AS $$
DECLARE
    cmd          record;
    v_project_id uuid;
    v_action     text;
    v_table      text;
    v_column     text;
    v_detail     jsonb;
    v_query      text := current_query();
BEGIN
    FOR cmd IN SELECT * FROM pg_event_trigger_ddl_commands() LOOP
        -- Only tenant schemas. Platform-owned DDL (public.* via the
        -- migrate Job) is out of scope.
        IF cmd.schema_name IS NULL OR cmd.schema_name NOT LIKE 'tenant\_%' ESCAPE '\' THEN
            CONTINUE;
        END IF;

        -- Resolve project_id.
        SELECT id INTO v_project_id
        FROM public.projects
        WHERE schema_name = cmd.schema_name;
        IF v_project_id IS NULL THEN
            CONTINUE;
        END IF;

        v_action := NULL;
        v_table  := NULL;
        v_column := NULL;
        v_detail := jsonb_build_object(
            'source',       'event_trigger',
            'command_tag',  cmd.command_tag,
            'object_type',  cmd.object_type,
            'object_id',    cmd.object_identity
        );

        -- ── CREATE TABLE ──
        IF cmd.command_tag = 'CREATE TABLE' AND cmd.object_type = 'table' THEN
            v_action := 'create_table';
            v_table  := split_part(cmd.object_identity, '.', 2);
            -- Pull the freshly-created column list for the detail.
            BEGIN
                v_detail := v_detail || jsonb_build_object(
                    'columns', COALESCE((
                        SELECT jsonb_agg(jsonb_build_object(
                            'name', attname,
                            'type', format_type(atttypid, atttypmod)
                        ) ORDER BY attnum)
                        FROM pg_attribute
                        WHERE attrelid = cmd.objid
                          AND attnum > 0
                          AND NOT attisdropped
                    ), '[]'::jsonb)
                );
            EXCEPTION WHEN OTHERS THEN
                -- Best-effort enrichment; if pg_attribute lookup fails
                -- we still log the bare create_table row.
                NULL;
            END;

        -- ── CREATE INDEX ──
        ELSIF cmd.command_tag = 'CREATE INDEX' AND cmd.object_type = 'index' THEN
            v_action := 'create_index';
            -- object_identity is 'schema.index_name'; the index's
            -- target table comes from pg_index.indrelid.
            BEGIN
                SELECT c.relname INTO v_table
                FROM pg_index i
                JOIN pg_class c ON c.oid = i.indrelid
                WHERE i.indexrelid = cmd.objid;
            EXCEPTION WHEN OTHERS THEN
                v_table := split_part(cmd.object_identity, '.', 2);
            END;

        -- ── ALTER TABLE … COLUMN … ──
        -- object_type='table column' covers ADD COLUMN, ALTER COLUMN
        -- TYPE, SET / DROP NOT NULL, SET / DROP DEFAULT, RENAME
        -- COLUMN. We disambiguate ADD vs alter-existing by sniffing
        -- current_query() — not bulletproof (multi-statement queries
        -- can mix forms) but good enough to label most operations
        -- correctly.
        ELSIF cmd.command_tag = 'ALTER TABLE' AND cmd.object_type = 'table column' THEN
            v_table  := split_part(cmd.object_identity, '.', 2);
            v_column := split_part(cmd.object_identity, '.', 3);
            IF v_query ~* '\yADD\s+COLUMN\y' THEN
                v_action := 'add_column';
            ELSE
                v_action := 'alter_column';
            END IF;
            -- Enrich detail with current column metadata.
            BEGIN
                v_detail := v_detail || COALESCE((
                    SELECT jsonb_build_object(
                        'type',     format_type(a.atttypid, a.atttypmod),
                        'nullable', NOT a.attnotnull,
                        'default',  pg_get_expr(d.adbin, d.adrelid)
                    )
                    FROM pg_attribute a
                    LEFT JOIN pg_attrdef d
                        ON d.adrelid = a.attrelid AND d.adnum = a.attnum
                    WHERE a.attrelid = cmd.objid AND a.attnum = cmd.objsubid
                ), '{}'::jsonb);
            EXCEPTION WHEN OTHERS THEN
                NULL;
            END;

        -- ── ALTER TABLE RENAME ──
        ELSIF cmd.command_tag = 'ALTER TABLE' AND cmd.object_type = 'table'
              AND v_query ~* '\yRENAME\s+TO\y' THEN
            v_action := 'rename_table';
            v_table  := split_part(cmd.object_identity, '.', 2);

        ELSE
            -- Everything else (generic ALTER TABLE without a column
            -- target, CREATE FUNCTION, CREATE TRIGGER, …): skip. The
            -- typed handlers' logSchemaChange continues to cover the
            -- platform DDL routes that care about these.
            CONTINUE;
        END IF;

        BEGIN
            INSERT INTO public.schema_changes
                (project_id, action, table_name, column_name, detail)
            VALUES
                (v_project_id, v_action, COALESCE(v_table, ''), v_column, v_detail);
        EXCEPTION WHEN OTHERS THEN
            -- Audit failure must never abort the user's DDL.
            RAISE WARNING 'log_ddl_event: insert failed: %', SQLERRM;
        END;
    END LOOP;
END;
$$;

-- ── sql_drop: DROP TABLE / COLUMN / INDEX ──────────────────────────
--
-- pg_event_trigger_dropped_objects() runs in the sql_drop event and
-- reports each object being dropped (object_type, object_name,
-- schema_name, …). We only care about drops in tenant schemas.
CREATE OR REPLACE FUNCTION public.log_ddl_drop()
RETURNS event_trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_catalog
AS $$
DECLARE
    obj          record;
    v_project_id uuid;
    v_action     text;
    v_table      text;
    v_column     text;
BEGIN
    FOR obj IN SELECT * FROM pg_event_trigger_dropped_objects() LOOP
        IF obj.schema_name IS NULL OR obj.schema_name NOT LIKE 'tenant\_%' ESCAPE '\' THEN
            CONTINUE;
        END IF;

        SELECT id INTO v_project_id
        FROM public.projects
        WHERE schema_name = obj.schema_name;
        IF v_project_id IS NULL THEN
            CONTINUE;
        END IF;

        v_action := NULL;
        v_table  := NULL;
        v_column := NULL;

        IF obj.object_type = 'table' THEN
            v_action := 'drop_table';
            v_table  := obj.object_name;
        ELSIF obj.object_type = 'table column' THEN
            v_action := 'drop_column';
            -- object_identity for table column is 'schema.table.column';
            -- object_name is the column itself.
            v_table  := split_part(obj.object_identity, '.', 2);
            v_column := obj.object_name;
        ELSIF obj.object_type = 'index' THEN
            v_action := 'drop_index';
            v_table  := obj.object_name;
        ELSE
            CONTINUE;
        END IF;

        BEGIN
            INSERT INTO public.schema_changes
                (project_id, action, table_name, column_name, detail)
            VALUES (
                v_project_id,
                v_action,
                COALESCE(v_table, ''),
                v_column,
                jsonb_build_object(
                    'source',      'event_trigger_drop',
                    'object_type', obj.object_type
                )
            );
        EXCEPTION WHEN OTHERS THEN
            RAISE WARNING 'log_ddl_drop: insert failed: %', SQLERRM;
        END;
    END LOOP;
END;
$$;

-- Ensure both functions are owned by the migrator so SECURITY DEFINER
-- runs with the right privileges (INSERT on public.schema_changes).
ALTER FUNCTION public.log_ddl_event()  OWNER TO eurobase_migrator;
ALTER FUNCTION public.log_ddl_drop()   OWNER TO eurobase_migrator;

-- Install the triggers. CREATE OR REPLACE EVENT TRIGGER is not a
-- thing, so drop-if-exists first for idempotent re-runs.
DROP EVENT TRIGGER IF EXISTS trg_log_ddl_end;
DROP EVENT TRIGGER IF EXISTS trg_log_ddl_drop;

CREATE EVENT TRIGGER trg_log_ddl_end
    ON ddl_command_end
    EXECUTE FUNCTION public.log_ddl_event();

CREATE EVENT TRIGGER trg_log_ddl_drop
    ON sql_drop
    EXECUTE FUNCTION public.log_ddl_drop();

COMMIT;
