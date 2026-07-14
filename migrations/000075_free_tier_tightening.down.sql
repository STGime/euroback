BEGIN;

-- Restore the pre-Phase-B Free caps.
UPDATE public.plan_limits
   SET mau_limit      = 10000,
       storage_mb     = 1024,
       bandwidth_mb   = 5120,
       ws_connections = 100
 WHERE plan = 'free';

ALTER TABLE public.plan_limits
    DROP COLUMN IF EXISTS quota_alerts,
    DROP COLUMN IF EXISTS byo_smtp,
    DROP COLUMN IF EXISTS custom_domain;

COMMIT;
