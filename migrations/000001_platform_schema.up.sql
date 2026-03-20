-- 000001_platform_schema.up.sql
-- Platform schema foundation: core tables for Eurobase BaaS.

BEGIN;

-- 1. Enable uuid-ossp extension
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- 2. platform_users
CREATE TABLE public.platform_users (
    id               UUID        PRIMARY KEY DEFAULT uuid_generate_v4(),
    hanko_user_id    TEXT        UNIQUE NOT NULL,
    email            TEXT        NOT NULL,
    display_name     TEXT,
    mollie_customer_id TEXT,
    plan             TEXT        DEFAULT 'free',
    created_at       TIMESTAMPTZ DEFAULT now()
);

-- 3. projects
CREATE TABLE public.projects (
    id           UUID        PRIMARY KEY DEFAULT uuid_generate_v4(),
    owner_id     UUID        NOT NULL REFERENCES public.platform_users(id) ON DELETE CASCADE,
    name         TEXT        NOT NULL,
    slug         TEXT        UNIQUE NOT NULL,
    schema_name  TEXT        UNIQUE NOT NULL,
    s3_bucket    TEXT        UNIQUE NOT NULL,
    region       TEXT        DEFAULT 'fr-par',
    plan         TEXT        DEFAULT 'free',
    status       TEXT        DEFAULT 'provisioning'
                             CHECK (status IN ('provisioning', 'active', 'suspended', 'deleting', 'provisioning_failed')),
    auth_config  JSONB       DEFAULT '{}',
    settings     JSONB       DEFAULT '{}',
    created_at   TIMESTAMPTZ DEFAULT now()
);

-- 4. api_keys
CREATE TABLE public.api_keys (
    id           UUID        PRIMARY KEY DEFAULT uuid_generate_v4(),
    project_id   UUID        NOT NULL REFERENCES public.projects(id) ON DELETE CASCADE,
    key_hash     TEXT        NOT NULL,
    key_prefix   TEXT        NOT NULL,
    type         TEXT        NOT NULL CHECK (type IN ('public', 'secret')),
    created_at   TIMESTAMPTZ DEFAULT now(),
    last_used_at TIMESTAMPTZ
);

-- 5. subscriptions
CREATE TABLE public.subscriptions (
    id                      UUID        PRIMARY KEY DEFAULT uuid_generate_v4(),
    project_id              UUID        NOT NULL REFERENCES public.projects(id) ON DELETE CASCADE,
    mollie_subscription_id  TEXT        UNIQUE,
    plan                    TEXT        NOT NULL,
    status                  TEXT        DEFAULT 'active'
                                        CHECK (status IN ('active', 'cancelled', 'overdue')),
    current_period_start    TIMESTAMPTZ,
    current_period_end      TIMESTAMPTZ,
    created_at              TIMESTAMPTZ DEFAULT now()
);

-- 6. invoices
CREATE TABLE public.invoices (
    id                UUID        PRIMARY KEY DEFAULT uuid_generate_v4(),
    project_id        UUID        NOT NULL REFERENCES public.projects(id) ON DELETE CASCADE,
    mollie_payment_id TEXT        UNIQUE,
    amount_cents      INTEGER     NOT NULL,
    currency          TEXT        DEFAULT 'EUR',
    status            TEXT        DEFAULT 'pending'
                                  CHECK (status IN ('pending', 'paid', 'failed', 'refunded')),
    paid_at           TIMESTAMPTZ,
    created_at        TIMESTAMPTZ DEFAULT now()
);

-- Indexes
CREATE INDEX idx_projects_owner_id     ON public.projects(owner_id);
CREATE INDEX idx_projects_slug         ON public.projects(slug);
CREATE INDEX idx_api_keys_project_id   ON public.api_keys(project_id);
CREATE INDEX idx_api_keys_key_hash     ON public.api_keys(key_hash);
CREATE INDEX idx_subscriptions_project_id ON public.subscriptions(project_id);
CREATE INDEX idx_invoices_project_id   ON public.invoices(project_id);

COMMIT;
