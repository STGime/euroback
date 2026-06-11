-- Tenant-level schema migrations (#190): platform-managed bookkeeping.
--
-- One row per (project, version) applied through
-- POST /platform/projects/{id}/migrations. Platform-managed so tenants
-- don't have to invent their own bookkeeping table — and so the obvious
-- name choice no longer collides with golang-migrate's schema_migrations.
-- The full SQL body is stored for auditability and checksum-verified on
-- re-apply (editing an applied migration is rejected; bump the version).

CREATE TABLE public.tenant_migrations (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    project_id  UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    version     BIGINT NOT NULL CHECK (version > 0),
    name        TEXT NOT NULL DEFAULT '',
    sql         TEXT NOT NULL,
    checksum    TEXT NOT NULL,
    applied_by  TEXT,
    applied_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, version)
);

CREATE INDEX idx_tenant_migrations_project ON public.tenant_migrations(project_id, version);

-- The gateway pool reads history (GET endpoint); writes happen inside
-- the migration transaction as eurobase_migrator (the table owner).
GRANT SELECT ON public.tenant_migrations TO eurobase_gateway;
GRANT SELECT, INSERT ON public.tenant_migrations TO eurobase_developer;
