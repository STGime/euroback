#!/usr/bin/env bash
# Read-only inventory of views in tenant schemas (shared cluster) that are
# not `security_invoker`, plus all materialized views. Such a view runs
# with its owner's privileges, so row-level security on the tables it reads
# is evaluated for the owner, not for the caller.
#
# One line per view:
#   VIEW <schema>.<name> kind=<view|materialized> project=<slug> plan=<plan>
#        rls_tables=<base tables with RLS on, or -> app_select=<yes|no>
# rls_tables lists base tables of the view that have RLS enabled (those
# are the ones whose policies the view does not apply). app_select says
# whether the project's app-facing roles (<schema>_func, eurobase_gateway)
# can SELECT the view.
#
# Team projects keep their tables on a dedicated database; they are only
# counted here, not inspected.
#
# Runs a one-off Job in-cluster as the developer role (same pattern as
# check-console-ddl-readiness.sh). Prints schema and object names only —
# never row data. This script never writes.
#
# Usage: ./scripts/ops/check-view-security.sh
set -euo pipefail
NS=eurobase
JOB=view-security-check

kubectl -n "$NS" delete job "$JOB" --ignore-not-found --wait=true >/dev/null 2>&1 || true
kubectl -n "$NS" apply -f - >/dev/null <<'YAML'
apiVersion: batch/v1
kind: Job
metadata: { name: view-security-check, namespace: eurobase }
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

printf "%s\n" "$OUT" | grep '^VIEW ' || true
echo ""
TOTAL=$(printf "%s\n" "$OUT" | grep -c '^VIEW ' || true)
EXPOSED=$(printf "%s\n" "$OUT" | grep '^VIEW ' | grep -v 'rls_tables=- ' | grep -c 'app_select=yes' || true)
MAT=$(printf "%s\n" "$OUT" | grep '^VIEW ' | grep -c 'kind=materialized' || true)
PROJECTS=$(printf "%s\n" "$OUT" | grep '^VIEW ' | sed -E 's/.* project=([^ ]+).*/\1/' | sort -u | grep -c . || true)
echo "Summary:"
echo "  schemas checked (shared cluster)                     : $(printf "%s\n" "$OUT" | sed -n 's/^SCHEMAS_CHECKED //p')"
echo "  views without security_invoker + materialized views  : $TOTAL (materialized: $MAT) in $PROJECTS project(s)"
echo "  of those reading RLS tables and selectable by the app: $EXPOSED"
echo "  Team projects (dedicated DB, not checked here)       : $(printf "%s\n" "$OUT" | sed -n 's/^TEAM_NOT_CHECKED //p')"
