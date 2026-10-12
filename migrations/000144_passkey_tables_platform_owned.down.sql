-- No-op. Ownership changes are not reverted (restoring the passkey tables to
-- _ddl would re-create the inconsistency this migration fixes). The function
-- redefinition is forward-compatible; reverting a specific object is a manual,
-- reviewed op. (Same stance as 000141/000143.)
SELECT 1;
