#!/usr/bin/env bash
# Read-only inventory of objects in tenant schemas (shared cluster) that run
# with their owner's rights, and so skip the row-level security of the
# tables they read: views that are not `security_invoker`, materialized
# views, SECURITY DEFINER functions/procedures and rules. New ones are
# refused by query.withOwnerRightsGuard; this lists what already exists.
#
# One line per object:
#   VIEW <schema>.<name> kind=<view|materialized> project=<slug> plan=<plan>
#        rls_tables=<base tables with RLS on, or -> app_select=<yes|no>
#   DEFINER <schema>.<function>() project=<slug> plan=<plan> app_execute=<yes|no>
#   RULE <schema>.<rule> on <table> project=<slug> plan=<plan>
# rls_tables lists base tables of a view that have RLS enabled. app_select /
# app_execute say whether the project's app-facing roles (<schema>_func,
# eurobase_gateway) can read the view / call the function.
#
# Team projects keep their tables on a dedicated database; they are only
# counted here, not inspected.
#
# Runs a one-off Job in-cluster as the developer role (same pattern as
# check-console-ddl-readiness.sh). Prints schema and object names only —
# never row data. This script never writes.
#
# Usage: ./scripts/ops/check-owner-rights-objects.sh
set -euo pipefail
NS=eurobase
JOB=owner-rights-check

kubectl -n "$NS" delete job "$JOB" --ignore-not-found --wait=true >/dev/null 2>&1 || true
kubectl -n "$NS" apply -f - >/dev/null <<'YAML'
apiVersion: batch/v1
kind: Job
metadata: { name: owner-rights-check, namespace: eurobase }
spec:
  backoffLimit: 0
  ttlSecondsAfterFinished: 200
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: c
          image: rg.fr-par.scw.cloud/eurobase-app/migrations:latest
          imagePullPolicy: Always
          command: ["sh", "-c", 'printf "%s\n" "$CHECK_SQL" | psql "$DATABASE_URL_DEVELOPER" -tA -v ON_ERROR_STOP=1']
          env:
            - name: DATABASE_URL_DEVELOPER
              valueFrom: { secretKeyRef: { name: eurobase-secrets, key: DATABASE_URL_DEVELOPER } }
            - name: CHECK_SQL
              value: |
                BEGIN READ ONLY;
                WITH v AS (
                  SELECT c.oid, n.nspname, c.relname, c.relkind, p.slug, p.plan
                    FROM pg_class c
                    JOIN pg_namespace n ON n.oid = c.relnamespace
                    JOIN public.projects p ON p.schema_name = n.nspname
                   WHERE c.relkind IN ('v', 'm')
                     AND (c.relkind = 'm' OR NOT EXISTS (
                           SELECT 1 FROM unnest(coalesce(c.reloptions, '{}'::text[])) o
                            WHERE o ~* '^security_invoker=(t|tr|tru|true|y|ye|yes|on|1)$'))
                )
                SELECT 'VIEW ' || v.nspname || '.' || v.relname
                       || ' kind=' || CASE v.relkind WHEN 'm' THEN 'materialized' ELSE 'view' END
                       || ' project=' || coalesce(v.slug, '?')
                       || ' plan=' || coalesce(v.plan::text, '?')
                       || ' rls_tables=' || coalesce((
                            SELECT string_agg(DISTINCT t.relname, ',' ORDER BY t.relname)
                              FROM pg_rewrite rw
                              JOIN pg_depend d ON d.classid = 'pg_rewrite'::regclass AND d.objid = rw.oid
                                              AND d.refclassid = 'pg_class'::regclass
                              JOIN pg_class t ON t.oid = d.refobjid
                             WHERE rw.ev_class = v.oid AND t.oid <> v.oid
                               AND t.relkind IN ('r', 'p') AND t.relrowsecurity), '-')
                       || ' app_select=' || CASE WHEN
                            (EXISTS (SELECT 1 FROM pg_roles r WHERE r.rolname = v.nspname || '_func')
                               AND has_table_privilege(v.nspname || '_func', v.oid, 'SELECT'))
                            OR has_table_privilege('eurobase_gateway', v.oid, 'SELECT')
                            THEN 'yes' ELSE 'no' END
                  FROM v
                 ORDER BY 1;
                SELECT 'DEFINER ' || n.nspname || '.' || p.proname || '()'
                       || ' project=' || coalesce(pr.slug, '?')
                       || ' plan=' || coalesce(pr.plan::text, '?')
                       || ' app_execute=' || CASE WHEN
                            (EXISTS (SELECT 1 FROM pg_roles r WHERE r.rolname = n.nspname || '_func')
                               AND has_function_privilege(n.nspname || '_func', p.oid, 'EXECUTE'))
                            OR has_function_privilege('eurobase_gateway', p.oid, 'EXECUTE')
                            THEN 'yes' ELSE 'no' END
                  FROM pg_proc p
                  JOIN pg_namespace n ON n.oid = p.pronamespace
                  JOIN public.projects pr ON pr.schema_name = n.nspname
                 WHERE p.prosecdef
                   AND p.proname <> ALL (ARRAY['auth_uid', 'auth_role', 'auth_email'])
                 ORDER BY 1;
                SELECT 'RULE ' || n.nspname || '.' || r.rulename || ' on ' || c.relname
                       || ' project=' || coalesce(pr.slug, '?')
                       || ' plan=' || coalesce(pr.plan::text, '?')
                  FROM pg_rewrite r
                  JOIN pg_class c ON c.oid = r.ev_class
                  JOIN pg_namespace n ON n.oid = c.relnamespace
                  JOIN public.projects pr ON pr.schema_name = n.nspname
                 WHERE r.rulename <> '_RETURN'
                 ORDER BY 1;
                SELECT 'TEAM_NOT_CHECKED ' || count(DISTINCT project_id)
                  FROM public.project_databases WHERE deleted_at IS NULL;
                SELECT 'SCHEMAS_CHECKED ' || count(*)
                  FROM public.projects p
                 WHERE p.schema_name IS NOT NULL
                   AND EXISTS (SELECT 1 FROM pg_namespace n WHERE n.nspname = p.schema_name);
                COMMIT;
YAML

kubectl -n "$NS" wait --for=condition=complete --timeout=120s "job/$JOB" >/dev/null 2>&1 || true
OUT=$(kubectl -n "$NS" logs "job/$JOB" 2>&1 || true)
kubectl -n "$NS" delete job "$JOB" --ignore-not-found >/dev/null 2>&1 || true

if ! printf "%s\n" "$OUT" | grep -q '^SCHEMAS_CHECKED '; then
  echo "The check did not complete. Job output:" >&2
  printf "%s\n" "$OUT" >&2
  exit 1
fi

printf "%s\n" "$OUT" | grep -E '^(VIEW|DEFINER|RULE) ' || true
echo ""
TOTAL=$(printf "%s\n" "$OUT" | grep -c '^VIEW ' || true)
EXPOSED=$(printf "%s\n" "$OUT" | grep '^VIEW ' | grep -v 'rls_tables=- ' | grep -c 'app_select=yes' || true)
MAT=$(printf "%s\n" "$OUT" | grep '^VIEW ' | grep -c 'kind=materialized' || true)
DEFINERS=$(printf "%s\n" "$OUT" | grep -c '^DEFINER ' || true)
DEFINERS_APP=$(printf "%s\n" "$OUT" | grep '^DEFINER ' | grep -c 'app_execute=yes' || true)
RULES=$(printf "%s\n" "$OUT" | grep -c '^RULE ' || true)
PROJECTS=$(printf "%s\n" "$OUT" | grep -E '^(VIEW|DEFINER|RULE) ' | sed -E 's/.* project=([^ ]+).*/\1/' | sort -u | grep -c . || true)
echo "Summary:"
echo "  schemas checked (shared cluster)                     : $(printf "%s\n" "$OUT" | sed -n 's/^SCHEMAS_CHECKED //p')"
echo "  projects with any of the objects below               : $PROJECTS"
echo "  views without security_invoker + materialized views  : $TOTAL (materialized: $MAT)"
echo "  of those reading RLS tables and selectable by the app: $EXPOSED"
echo "  SECURITY DEFINER functions (callable by the app)     : $DEFINERS ($DEFINERS_APP)"
echo "  rules                                                : $RULES"
echo "  Team projects (dedicated DB, not checked here)       : $(printf "%s\n" "$OUT" | sed -n 's/^TEAM_NOT_CHECKED //p')"
