-- Ownership convergence is a one-way consolidation: there is no safe,
-- correct inverse (the prior owner of each object is not recorded, and
-- splitting ownership back across migrator / gateway / developer would not
-- match any real prior state). Drop only the helper; leave ownership as is.
DROP FUNCTION IF EXISTS public.converge_tenant_ownership(TEXT);
