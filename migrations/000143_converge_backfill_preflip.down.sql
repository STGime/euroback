-- No-op. This migration only re-ran the idempotent convergence backfill; it
-- created nothing to drop. Ownership is NOT reverted: once a developer edits
-- (CREATE OR REPLACE / ALTER) a reassigned object as `_ddl`, forcing it back
-- to the migrator could break their project. Reverting a specific object is a
-- manual, reviewed op. (Same stance as 000141's down migration.)
SELECT 1;
