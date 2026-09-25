ALTER TABLE public.export_requests
    DROP COLUMN IF EXISTS warnings,
    DROP COLUMN IF EXISTS complete;
