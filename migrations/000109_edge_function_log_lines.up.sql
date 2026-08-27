-- 000109_edge_function_log_lines.up.sql
--
-- Adds a JSONB column on public.edge_function_logs to persist the
-- structured lines a function emits via ctx.log.info/warn/error
-- (see functions-runner/server.ts createLogCapture). Before this
-- migration the runner buffered log lines in memory, mirrored them
-- to the pod's stdout, and garbage-collected them at invocation
-- end — customer-invisible unless they had kubectl access.
-- Closes #492 on the persistence side; the runner/gateway/console
-- wiring lands in the same PR that ships this migration.
--
-- Shape: JSONB array of { level, msg, data?, ts } objects, capped
-- by the runner at LOG_OUTPUT_LIMIT (10 KB per invocation total —
-- functions-runner/server.ts LOG_OUTPUT_LIMIT). Storing the array
-- inline rather than in a sibling table keeps the read path a
-- single-row query (edge_function_logs is already indexed by
-- (function_id, created_at DESC) for the recent-invocations view)
-- and lets the existing per-row retention story cover log lines
-- automatically once we ship one. If per-line grep across
-- invocations later becomes a common ask, the follow-up migrates
-- to a sibling table (see #492 body for the trade-off).
--
-- Backfill is intentionally skipped — pre-migration invocations
-- never had log lines to persist. NULL means "no lines captured"
-- (never invoked, or invocation had no ctx.log.* calls); an empty
-- array means "invoked with ctx.log.* calls but they were all
-- truncated or discarded." The console distinguishes both.

BEGIN;

ALTER TABLE public.edge_function_logs
    ADD COLUMN IF NOT EXISTS log_lines JSONB;

-- Deliberately no GIN index. Current UI queries the array by row
-- (fetch the last 50 invocations, render their lines) — the array
-- is small (≤ 10 KB) and read in bulk, so a per-row seq-scan of
-- the array is fine. A GIN index would slow every insert without
-- payoff until per-line grep-across-invocations is a shipped
-- feature. Filing as a follow-up if that pattern lands.

COMMIT;
