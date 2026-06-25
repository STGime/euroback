# Enabling tenant migrations (#190) in production

Tenant migrations (`POST /platform/projects/{id}/migrations`, `eurobase migrations up`)
ship dormant and **fail closed (503)** until two one-time ops steps are done.
Both are safe to do independently; the feature only works once both are complete.

## Step 1 — `DDL_PASSWORD_SECRET` (done via script)

The gateway derives each per-tenant ddl role's login password from this secret.

```
./scripts/ops/set-ddl-password-secret.sh
```

Generates a 32-byte secret, patches `eurobase-secrets`, and restarts the gateway.
Confirm with the gateway log line `"tenant migrations enabled"`.

## Step 2 — let `eurobase_migrator` grant CONNECT (needs `_rdb_superadmin`)

The per-tenant `tenant_<id>_ddl` roles are LOGIN roles the gateway connects as.
They are created by the SQL migration `provision_tenant_ddl_role`, so — unlike the
runtime roles created through the Scaleway console — they do **not** get Scaleway's
automatic CONNECT grant. `eurobase_migrator` cannot grant it onward (it has CONNECT
but no grant option), and the `eurobase` database is owned by `_rdb_superadmin`,
which customers cannot log in as or `SET ROLE` to.

So one statement must be run **as `_rdb_superadmin`** — which on Scaleway managed
Postgres means a support request.

### Scaleway support ticket (copy-paste)

> Subject: Run a one-time GRANT as the database superadmin on our PostgreSQL instance
>
> Instance: **eurobase-db** (`62deb4e1-4dba-44ea-8fbc-2d519637425a`), region **fr-par**, PostgreSQL 16.
> Database: **eurobase**.
>
> Please run the following as the database owner / `_rdb_superadmin`:
>
> ```sql
> GRANT CONNECT ON DATABASE eurobase TO eurobase_migrator WITH GRANT OPTION;
> ```
>
> (Equivalent alternative if you prefer transferring ownership:
> `ALTER DATABASE eurobase OWNER TO eurobase_migrator;`)
>
> Context: `eurobase_migrator` is our deploy/migration role (already a Scaleway
> "admin" user). It needs to forward CONNECT to per-tenant roles our migrations
> create dynamically. It currently has CONNECT but no grant option, and the
> database is owned by `_rdb_superadmin`, so we can't run this ourselves.

### After Scaleway completes it

Verify the grant took (read-only, from inside the cluster):

```
./scripts/ops/db-checks.sh   # prints migrator_canconnectgrant=true when done
```

No redeploy needed — the gateway re-grants and verifies each tenant's CONNECT on
its first `eurobase migrations up`.

## Verify the feature end to end

From a project with a PAT:

```
eurobase login --token "$EUROBASE_PAT"
eurobase switch <project>
eurobase migrations new create_example
$EDITOR migrations/0001_create_example.sql
eurobase migrations up
```

## Recovery: a failed/dirty migrate deploy

The migrate Job gates the rollout, so a failed migration leaves production on the
previous version and `schema_migrations` dirty. Clear it before redeploying:

```
./scripts/ops/migrate-status.sh   # shows the version + dirty flag
./scripts/ops/migrate-force.sh    # forces the last-good version (default 62)
```

See also: the isolation proof `scripts/verify-tenant-migration-isolation.sh`, and
the prod-vs-local Postgres role-default notes in the team memory.
