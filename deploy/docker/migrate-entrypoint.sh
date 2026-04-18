#!/bin/sh
# Migrations entrypoint.
#
# Before running `migrate up`, idempotently transfer ownership of anything
# still owned by eurobase_api to eurobase_migrator. This exists because
# migrations that ALTER platform-owned tables fail with "must be owner of
# table X" until the role split has propagated — and the REASSIGN itself
# lives inside migration 000037, creating a chicken-and-egg if any earlier
# migration also needs migrator ownership.
#
# The block is a no-op once eurobase_api is gone or no longer owns anything.
# Skipped entirely if eurobase_api doesn't exist (post-cutover state).
set -eu

if [ -z "${DATABASE_URL_MIGRATOR:-}" ]; then
    echo "DATABASE_URL_MIGRATOR is not set" >&2
    exit 1
fi

# Ownership bootstrap. Runs as eurobase_migrator; the GRANT only works if
# the migrator role has ADMIN OPTION on eurobase_api (granted the first
# time this runs — either by a manual bootstrap step or by a prior run of
# this script). The DO block is idempotent.
psql "$DATABASE_URL_MIGRATOR" -v ON_ERROR_STOP=1 <<'SQL'
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'eurobase_api') THEN
        BEGIN
            EXECUTE 'GRANT eurobase_api TO eurobase_migrator';
        EXCEPTION WHEN insufficient_privilege THEN
            RAISE NOTICE 'GRANT eurobase_api TO eurobase_migrator skipped (already a member or no ADMIN OPTION)';
        END;
        EXECUTE 'REASSIGN OWNED BY eurobase_api TO eurobase_migrator';
    END IF;
END
$$;
SQL

# Delegate everything else to the migrate binary. The Job manifest passes
# `-database $(DATABASE_URL_MIGRATOR) up` as args.
exec migrate -path /migrations "$@"
