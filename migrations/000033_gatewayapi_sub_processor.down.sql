-- 000033_gatewayapi_sub_processor.down.sql
DELETE FROM service_dependencies WHERE eurobase_feature = 'sms';
DELETE FROM sub_processors WHERE name = 'GatewayAPI' AND service = 'Transactional SMS';
