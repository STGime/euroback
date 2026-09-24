-- Reverse of 000126 is intentionally a no-op for the runner revokes: the
-- removed grants were historic, out-of-band access that must not be
-- restored. Only the gateway EXECUTE grants are left in place too (the
-- gateway needs them). Nothing to undo.
SELECT 1;
