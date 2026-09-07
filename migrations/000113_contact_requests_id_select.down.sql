-- 000113_contact_requests_id_select.down.sql

REVOKE SELECT (id) ON public.contact_requests FROM eurobase_gateway;
