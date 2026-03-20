-- 000001_platform_schema.down.sql
-- Reverse platform schema foundation: drop all tables and extension.

BEGIN;

DROP TABLE IF EXISTS public.invoices;
DROP TABLE IF EXISTS public.subscriptions;
DROP TABLE IF EXISTS public.api_keys;
DROP TABLE IF EXISTS public.projects;
DROP TABLE IF EXISTS public.platform_users;

DROP EXTENSION IF EXISTS "uuid-ossp";

COMMIT;
