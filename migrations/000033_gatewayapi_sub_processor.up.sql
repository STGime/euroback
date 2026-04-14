-- 000033_gatewayapi_sub_processor.up.sql
-- Add GatewayAPI (SMS provider) as a sub-processor for GDPR compliance.

BEGIN;

INSERT INTO sub_processors (name, legal_entity, country, country_code, jurisdiction, service, purpose, data_categories, data_subjects, transfer_mechanism, security_certs, dpa_url, privacy_url, cloud_act_risk, active)
VALUES (
    'GatewayAPI',
    'OnlineCity ApS',
    'Denmark',
    'DK',
    'EU',
    'Transactional SMS',
    'Delivers SMS OTP verification codes for phone-based authentication',
    ARRAY['phone_number'],
    'End-users authenticating via phone number',
    'EU internal (no third-country transfer)',
    ARRAY['ISO 27001'],
    'https://gatewayapi.com/dpa/',
    'https://gatewayapi.com/privacy/',
    false,
    true
)
ON CONFLICT DO NOTHING;

-- Link GatewayAPI to the 'sms' feature.
INSERT INTO service_dependencies (eurobase_feature, sub_processor_id)
SELECT 'sms', id FROM sub_processors WHERE name = 'GatewayAPI' AND service = 'Transactional SMS'
ON CONFLICT DO NOTHING;

COMMIT;
