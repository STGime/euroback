-- For Scaleway support, run as _rdb_superadmin on the `eurobase` database.
--
-- Removes eurobase_function_runner's membership in every per-tenant
-- `<schema>_func` role (stage B phase 1b, migration 000127). On prod these
-- memberships are recorded with grantor _rdb_superadmin, so
-- eurobase_migrator cannot revoke them (PG16: only roles with the
-- grantor's privileges may). The runner no longer uses them: customer SQL
-- logs in as the tenant's own role.
--
-- Idempotent. Safe while the platform is running.

DO $$
DECLARE
    m record;
    n int := 0;
BEGIN
    FOR m IN
        SELECT r.rolname, pg_get_userbyid(am.grantor) AS grantor
          FROM pg_auth_members am
          JOIN pg_roles r ON r.oid = am.roleid
         WHERE am.member = 'eurobase_function_runner'::regrole
           AND r.rolname ~ '^tenant_[0-9a-f_]+_func$'
    LOOP
        EXECUTE format('REVOKE %I FROM eurobase_function_runner GRANTED BY %I', m.rolname, m.grantor);
        n := n + 1;
    END LOOP;
    RAISE NOTICE 'revoked % memberships', n;
END$$;

-- Expect 0.
SELECT count(*) AS remaining_runner_tenant_memberships
  FROM pg_auth_members am
  JOIN pg_roles r ON r.oid = am.roleid
 WHERE am.member = 'eurobase_function_runner'::regrole
   AND r.rolname ~ '^tenant_[0-9a-f_]+_func$';
