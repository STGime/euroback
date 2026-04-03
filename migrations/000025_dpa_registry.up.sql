BEGIN;

CREATE TABLE public.sub_processors (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    name            TEXT NOT NULL,
    legal_entity    TEXT NOT NULL,
    country         TEXT NOT NULL,
    country_code    TEXT NOT NULL,
    jurisdiction    TEXT NOT NULL,
    service         TEXT NOT NULL,
    purpose         TEXT NOT NULL,
    data_categories TEXT[] NOT NULL,
    data_subjects   TEXT NOT NULL DEFAULT 'End-users of customer applications',
    transfer_mechanism TEXT NOT NULL DEFAULT 'none',
    security_certs  TEXT[],
    dpa_url         TEXT,
    privacy_url     TEXT,
    cloud_act_risk  BOOLEAN NOT NULL DEFAULT false,
    added_at        TIMESTAMPTZ DEFAULT now(),
    updated_at      TIMESTAMPTZ DEFAULT now(),
    active          BOOLEAN DEFAULT true
);

CREATE TABLE public.service_dependencies (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    eurobase_feature TEXT NOT NULL,
    sub_processor_id UUID NOT NULL REFERENCES sub_processors(id) ON DELETE CASCADE,
    required        BOOLEAN DEFAULT true
);

-- Seed: Scaleway services
INSERT INTO sub_processors (name, legal_entity, country, country_code, jurisdiction, service, purpose, data_categories, security_certs, dpa_url, privacy_url, cloud_act_risk)
VALUES
('Scaleway SAS', 'SAS (Société par Actions Simplifiée)', 'France', 'FR', 'EU', 'database', 'Managed PostgreSQL database hosting', ARRAY['application_data', 'user_credentials', 'email_addresses'], ARRAY['ISO 27001', 'HDS', 'SecNumCloud'], 'https://www.scaleway.com/en/contracts/', 'https://www.scaleway.com/en/privacy-policy/', false),
('Scaleway SAS', 'SAS (Société par Actions Simplifiée)', 'France', 'FR', 'EU', 'storage', 'S3-compatible object storage', ARRAY['files', 'documents', 'media'], ARRAY['ISO 27001', 'HDS'], 'https://www.scaleway.com/en/contracts/', 'https://www.scaleway.com/en/privacy-policy/', false),
('Scaleway SAS', 'SAS (Société par Actions Simplifiée)', 'France', 'FR', 'EU', 'email', 'Transactional email delivery (TEM)', ARRAY['email_addresses', 'email_content'], ARRAY['ISO 27001'], 'https://www.scaleway.com/en/contracts/', 'https://www.scaleway.com/en/privacy-policy/', false),
('Scaleway SAS', 'SAS (Société par Actions Simplifiée)', 'France', 'FR', 'EU', 'compute', 'Kubernetes compute (Kapsule)', ARRAY['application_data', 'request_logs', 'ip_addresses'], ARRAY['ISO 27001', 'HDS', 'SecNumCloud'], 'https://www.scaleway.com/en/contracts/', 'https://www.scaleway.com/en/privacy-policy/', false),
('Scaleway SAS', 'SAS (Société par Actions Simplifiée)', 'France', 'FR', 'EU', 'cache', 'Managed Redis for rate limiting and real-time', ARRAY['session_data', 'rate_limit_counters'], ARRAY['ISO 27001'], 'https://www.scaleway.com/en/contracts/', 'https://www.scaleway.com/en/privacy-policy/', false);

-- Seed: Mollie (billing, future)
INSERT INTO sub_processors (name, legal_entity, country, country_code, jurisdiction, service, purpose, data_categories, security_certs, dpa_url, privacy_url, cloud_act_risk)
VALUES
('Mollie B.V.', 'B.V. (Besloten Vennootschap)', 'Netherlands', 'NL', 'EU', 'billing', 'Payment processing and subscription management', ARRAY['email_addresses', 'payment_data', 'billing_addresses'], ARRAY['PCI DSS Level 1'], 'https://www.mollie.com/gb/privacy', 'https://www.mollie.com/gb/privacy', false);

-- Seed: OAuth providers (conditional — only when enabled by customer)
INSERT INTO sub_processors (name, legal_entity, country, country_code, jurisdiction, service, purpose, data_categories, data_subjects, transfer_mechanism, security_certs, dpa_url, privacy_url, cloud_act_risk)
VALUES
('Google LLC', 'LLC', 'United States', 'US', 'US', 'oauth_google', 'OAuth identity verification (sign-in only)', ARRAY['email_addresses', 'display_name'], 'End-users who choose Google sign-in', 'EU-US Data Privacy Framework', ARRAY['ISO 27001', 'SOC 2'], 'https://cloud.google.com/terms/data-processing-addendum', 'https://policies.google.com/privacy', true),
('GitHub Inc. (Microsoft)', 'Inc.', 'United States', 'US', 'US', 'oauth_github', 'OAuth identity verification (sign-in only)', ARRAY['email_addresses', 'username'], 'End-users who choose GitHub sign-in', 'EU-US Data Privacy Framework', ARRAY['SOC 2'], 'https://github.com/customer-terms/github-data-protection-agreement', 'https://docs.github.com/en/site-policy/privacy-policies/github-general-privacy-statement', true);

-- Service dependencies
-- Required (always active for every project)
INSERT INTO service_dependencies (eurobase_feature, sub_processor_id, required)
SELECT 'database', id, true FROM sub_processors WHERE service = 'database';
INSERT INTO service_dependencies (eurobase_feature, sub_processor_id, required)
SELECT 'compute', id, true FROM sub_processors WHERE service = 'compute';
INSERT INTO service_dependencies (eurobase_feature, sub_processor_id, required)
SELECT 'cache', id, true FROM sub_processors WHERE service = 'cache';

-- Conditional (only when feature is used)
INSERT INTO service_dependencies (eurobase_feature, sub_processor_id, required)
SELECT 'storage', id, false FROM sub_processors WHERE service = 'storage';
INSERT INTO service_dependencies (eurobase_feature, sub_processor_id, required)
SELECT 'email', id, false FROM sub_processors WHERE service = 'email';
INSERT INTO service_dependencies (eurobase_feature, sub_processor_id, required)
SELECT 'billing', id, false FROM sub_processors WHERE service = 'billing';
INSERT INTO service_dependencies (eurobase_feature, sub_processor_id, required)
SELECT 'oauth_google', id, false FROM sub_processors WHERE service = 'oauth_google';
INSERT INTO service_dependencies (eurobase_feature, sub_processor_id, required)
SELECT 'oauth_github', id, false FROM sub_processors WHERE service = 'oauth_github';

COMMIT;
