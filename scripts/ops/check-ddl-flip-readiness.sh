#!/usr/bin/env bash
# Read-only pre-flip check for PLATFORM_DDL_LOGIN (step 6e).
#
# Flipping PLATFORM_DDL_LOGIN moves console / MCP / transaction / schema-DDL
# SQL from eurobase_migrator onto each shared-cluster project's own
# `<schema>_ddl` login. The behaviour change that matters:
#
#   * `_ddl` OWNS the tenant's application tables  -> it BYPASSES their RLS
#     (same as, or more than, migrator does today). Reads/writes can only
#     gain rows, never lose them. No failure risk there.
#   * `_ddl` is a NON-OWNER of the migrator-owned system tables
#     (users, user_identities, refresh_tokens, email_tokens, storage_objects,
#     storage_shared_prefixes, vault_secrets, user_passkey_credentials,
#     webauthn_challenges) -> it is RLS-SUBJECT on them, where migrator
#     (the owner) bypassed. The console keeps working ONLY because every
#     system-table policy admits `public.is_service_role()` and the platform
#     path sets app.end_user_role='service'. If a tenant drifted — a policy
#     without the service branch, RLS enabled with no admitting policy, a
#     missing EXECUTE grant on the helper, or missing DML on the system table
#     — the console would start erroring for that tenant after the flip.
#
# This asserts, per provisioned shared-cluster tenant, that none of that is
# true. It NEVER writes. Exit 0 = ready to flip; exit 1 = findings to fix.
#
# Prerequisite for the DML check (category E): DDL_PERSISTENT_LOGIN must
# already be enabled on the WORKER, so tenantlogin.DDLEnsurer has granted
# each `_ddl` the system-table DML. Run this AFTER the worker flag, BEFORE
# the gateway flag — that is the correct 6e ordering.
#
# Usage: ./scripts/ops/check-ddl-flip-readiness.sh
set -euo pipefail
NS=eurobase
JOB=ddl-flip-readiness-check

kubectl -n "$NS" delete job "$JOB" --ignore-not-found --wait=true >/dev/null 2>&1 || true
kubectl -n "$NS" apply -f - >/dev/null <<'YAML'
apiVersion: batch/v1
kind: Job
metadata: { name: ddl-flip-readiness-check, namespace: eurobase }
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
          command: ["sh", "-c", 'printf "%s\n" "$CHECK_SQL" | psql "$DATABASE_URL_DEVELOPER" -tA']
          env:
            - name: DATABASE_URL_DEVELOPER
              valueFrom: { secretKeyRef: { name: eurobase-secrets, key: DATABASE_URL_DEVELOPER } }
            - name: CHECK_SQL
              value: |
                \set ON_ERROR_STOP on
                -- ONE explicit transaction so the temp table survives across
                -- statements (psql is autocommit; a bare CREATE TEMP … ON
                -- COMMIT DROP would drop immediately and the scan would abort
                -- with no findings — which must NOT read as "ready"). The
                -- final SCAN_COMPLETE sentinel is required by the wrapper.
                BEGIN;
                CREATE TEMP TABLE _systab(name text) ON COMMIT DROP;
                INSERT INTO _systab VALUES
                  ('users'),('user_identities'),('refresh_tokens'),('email_tokens'),
                  ('storage_objects'),('storage_shared_prefixes'),('vault_secrets'),
                  ('user_passkey_credentials'),('webauthn_challenges');

                -- Shared-cluster tenants only. A Team/dedicated project (a
                -- live project_databases row) routes console SQL to its
                -- dedicated instance owner, NOT a `_ddl` login — a stale
                -- shared-cluster schema it may have left behind is never
                -- served by the flip, so it must not block it.
                CREATE TEMP TABLE _schemas ON COMMIT DROP AS
                  SELECT p.schema_name FROM public.projects p
                   WHERE p.schema_name IS NOT NULL
                     AND NOT EXISTS (SELECT 1 FROM public.project_databases pd
                                      WHERE pd.project_id = p.id AND pd.state = 'active');

                -- NOTE: the C classifier below assumes the system-table
                -- policies are PERMISSIVE (as provision_tenant creates them).
                -- A hand-made RESTRICTIVE policy is not modelled; none exist
                -- on prod system tables (verified 2026-10-11).

                -- A: migrator-owned APPLICATION tables (non-system) still in a
                --    tenant schema. These flip from owner-bypass to RLS-subject
                --    under _ddl. 6c convergence should have reassigned them to
                --    _ddl. Want: none.
                SELECT 'A_UNCONVERGED_TABLE ' || n.nspname || '.' || c.relname
                  FROM pg_class c
                  JOIN pg_namespace n ON n.oid = c.relnamespace
                  JOIN pg_roles r ON r.oid = c.relowner
                 WHERE n.nspname IN (SELECT schema_name FROM _schemas)
                   AND c.relkind IN ('r','p')
                   AND r.rolname = 'eurobase_migrator'
                   AND c.relname NOT IN (SELECT name FROM _systab)
                 ORDER BY 1;

                -- B: tenant tables owned by gateway/developer. _ddl is a
                --    non-owner -> RLS-subject. Need a Scaleway REASSIGN first.
                --    Want: none.
                SELECT 'B_FOREIGN_TABLE ' || n.nspname || '.' || c.relname || ' owner=' || r.rolname
                  FROM pg_class c
                  JOIN pg_namespace n ON n.oid = c.relnamespace
                  JOIN pg_roles r ON r.oid = c.relowner
                 WHERE n.nspname IN (SELECT schema_name FROM _schemas)
                   AND c.relkind IN ('r','p')
                   AND r.rolname IN ('eurobase_gateway','eurobase_developer')
                 ORDER BY 1;

                -- C: a system-table policy that does NOT admit the console
                --    `_ddl` path. That path sets the service GUC but NOT the
                --    internal-auth GUC, so a policy admits `_ddl` iff it
                --    references is_service_role() OR is unconditionally `true`.
                --    EXCLUDED (not a blocker):
                --      * the #164 credential-table policies — is_internal_auth_path()
                --        on refresh_tokens / email_tokens / vault_secrets. These
                --        INTENTIONALLY deny the generic SQL path; after the flip
                --        console/MCP SQL (as RLS-subject `_ddl`) is correctly
                --        refused raw access — the mitigation finally biting.
                --        Reported as INFO_AUTH_DENY below.
                --    A row here is a genuine drift: a system-table policy that
                --    neither admits service nor is `true` nor is an intended
                --    credential deny. Want: none.
                SELECT 'C_POLICY_NO_SERVICE ' || schemaname || '.' || tablename || ' ' || policyname
                  FROM pg_policies p
                  JOIN _systab s ON s.name = p.tablename
                 WHERE p.schemaname IN (SELECT schema_name FROM _schemas)
                   AND position('is_service_role()' in coalesce(p.qual,''))       = 0
                   AND position('is_service_role()' in coalesce(p.with_check,'')) = 0
                   AND btrim(coalesce(p.qual,'')) <> 'true'
                   AND btrim(coalesce(p.with_check,'')) <> 'true'  -- INSERT-only admit-all
                   AND NOT (p.tablename IN ('refresh_tokens','email_tokens','vault_secrets')
                            AND (position('is_internal_auth_path()' in coalesce(p.qual,'')) > 0
                                 OR position('is_internal_auth_path()' in coalesce(p.with_check,'')) > 0))
                 ORDER BY 1;

                -- INFO (not a blocker): the #164 intended-deny credential-table
                -- policies. Counted separately so they never keep the check red.
                SELECT 'INFO_AUTH_DENY ' || schemaname || '.' || tablename || ' ' || policyname
                  FROM pg_policies p
                 WHERE p.schemaname IN (SELECT schema_name FROM _schemas)
                   AND p.tablename IN ('refresh_tokens','email_tokens','vault_secrets')
                   AND (position('is_internal_auth_path()' in coalesce(p.qual,'')) > 0
                        OR position('is_internal_auth_path()' in coalesce(p.with_check,'')) > 0)
                 ORDER BY 1;

                -- C2: a system table with RLS enabled but ZERO policies at all
                --     -> default-deny for the non-owner _ddl. Want: none.
                SELECT 'C2_RLS_NO_POLICY ' || n.nspname || '.' || c.relname
                  FROM pg_class c
                  JOIN pg_namespace n ON n.oid = c.relnamespace
                  JOIN _systab s ON s.name = c.relname
                 WHERE n.nspname IN (SELECT schema_name FROM _schemas)
                   AND c.relkind = 'r' AND c.relrowsecurity
                   AND NOT EXISTS (SELECT 1 FROM pg_policy pol WHERE pol.polrelid = c.oid)
                 ORDER BY 1;

                -- D: a _ddl role missing EXECUTE on a policy helper, or USAGE
                --    on public -> every system-table access errors under _ddl.
                --    Want: none.
                SELECT 'D_DDL_MISSING_GRANT ' || (p.schema_name || '_ddl') || ' ' || grant_missing
                  FROM public.projects p
                  CROSS JOIN LATERAL (VALUES
                      ('USAGE public',            has_schema_privilege(p.schema_name || '_ddl', 'public', 'USAGE')),
                      ('EXECUTE is_service_role', has_function_privilege(p.schema_name || '_ddl', 'public.is_service_role()', 'EXECUTE')),
                      ('EXECUTE current_end_user_id', has_function_privilege(p.schema_name || '_ddl', 'public.current_end_user_id()', 'EXECUTE'))
                  ) AS g(grant_missing, present)
                 WHERE p.schema_name IN (SELECT schema_name FROM _schemas)
                   AND EXISTS (SELECT 1 FROM pg_roles r WHERE r.rolname = p.schema_name || '_ddl')
                   AND NOT g.present
                 ORDER BY 1;

                -- E: a _ddl role missing SELECT/INSERT/UPDATE/DELETE on one of
                --    its system tables -> the console's writes to that table
                --    fail under _ddl. These are granted by DDLEnsurer once
                --    DDL_PERSISTENT_LOGIN is on the WORKER. If this lists rows,
                --    the worker flag has not taken effect for that tenant yet.
                --    Want: none (AFTER the worker flag).
                SELECT 'E_DDL_MISSING_DML ' || (p.schema_name || '_ddl') || ' ' || p.schema_name || '.' || s.name || ' ' || perm
                  FROM public.projects p
                  JOIN _systab s ON true
                  CROSS JOIN LATERAL (VALUES ('SELECT'),('INSERT'),('UPDATE'),('DELETE')) AS perms(perm)
                 WHERE p.schema_name IN (SELECT schema_name FROM _schemas)
                   AND EXISTS (SELECT 1 FROM pg_roles r WHERE r.rolname = p.schema_name || '_ddl')
                   AND to_regclass(p.schema_name || '.' || s.name) IS NOT NULL
                   AND NOT has_table_privilege(p.schema_name || '_ddl', p.schema_name || '.' || s.name, perm)
                 ORDER BY 1;

                -- Required sentinel: proves every query above ran. Without it
                -- the wrapper treats the scan as failed, never as "ready".
                SELECT 'SCAN_COMPLETE';
                ROLLBACK;
YAML

kubectl -n "$NS" wait --for=condition=complete --timeout=180s "job/$JOB" >/dev/null 2>&1 || true
PHASE=$(kubectl -n "$NS" get pods -l job-name="$JOB" -o jsonpath='{.items[0].status.phase}' 2>/dev/null || true)
OUT=$(kubectl -n "$NS" logs "job/$JOB" 2>&1 || true)
kubectl -n "$NS" delete job "$JOB" --ignore-not-found >/dev/null 2>&1 || true

# Fail closed: the scan must have finished cleanly AND emitted its sentinel.
# A psql error (aborted scan) yields no findings, which must never read READY.
if [ "$PHASE" != "Succeeded" ] || ! printf '%s' "$OUT" | grep -q '^SCAN_COMPLETE$'; then
  echo "SCAN DID NOT COMPLETE (job phase=$PHASE, sentinel missing) — treat as NOT READY." >&2
  echo "----- raw output -----" >&2
  printf '%s\n' "$OUT" >&2
  exit 2
fi

echo "$OUT" | grep -E '^(A_|B_|C_|C2_|D_|E_)' | sort || true
echo ""
A=$(printf "%s\n" "$OUT" | grep -c '^A_UNCONVERGED_TABLE '  || true)
B=$(printf "%s\n" "$OUT" | grep -c '^B_FOREIGN_TABLE '      || true)
C=$(printf "%s\n" "$OUT" | grep -c '^C_POLICY_NO_SERVICE '  || true)
C2=$(printf "%s\n" "$OUT" | grep -c '^C2_RLS_NO_POLICY '    || true)
D=$(printf "%s\n" "$OUT" | grep -c '^D_DDL_MISSING_GRANT '  || true)
E=$(printf "%s\n" "$OUT" | grep -c '^E_DDL_MISSING_DML '    || true)
INFO=$(printf "%s\n" "$OUT" | grep -c '^INFO_AUTH_DENY '    || true)

echo "Summary (blockers — want 0):"
echo "  A unconverged migrator-owned app tables (RLS flip)      : $A"
echo "  B gateway/developer-owned tenant tables (need REASSIGN) : $B"
echo "  C system-table policies that don't admit _ddl (drift)   : $C"
echo "  C2 system tables with RLS but no policy (default-deny)   : $C2"
echo "  D _ddl roles missing a helper EXECUTE / public USAGE     : $D"
echo "  E _ddl roles missing system-table DML (worker flag?)     : $E"
echo "Informational (NOT a blocker):"
echo "  #164 credential-table deny (intended; flip enforces it)  : $INFO"
echo ""

# A–D are hard blockers. E is informational until the worker flag is on;
# it MUST be 0 before the gateway flag flips, so it blocks too.
if [ "$A$B$C$C2$D$E" != "000000" ]; then
  echo "NOT READY: resolve the findings above before flipping PLATFORM_DDL_LOGIN." >&2
  [ "$E" != "0" ] && echo "  (E>0 usually means DDL_PERSISTENT_LOGIN is not yet active on the worker.)" >&2
  exit 1
fi
echo "READY: every shared-cluster tenant admits the service role under its _ddl login."
echo "       Safe to flip PLATFORM_DDL_LOGIN on the gateway (with DDL_PERSISTENT_LOGIN already on)."
