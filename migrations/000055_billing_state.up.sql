-- 000055_billing_state.up.sql
--
-- Add the state fields the dunning + cancel-at-period-end flows need.
-- Existing `subscriptions` table (from migration 000001) tracks plan +
-- status but has no way to mark a subscription as "user requested
-- cancellation, ride out the paid period" or "payment failed, in grace
-- before downgrade". These two columns close that gap.
--
-- No explicit BEGIN/COMMIT — golang-migrate wraps each file in its own
-- transaction. Inline COMMIT would commit the runner's outer tx early
-- (same footgun as #121 / #145's migration 000052).

ALTER TABLE public.subscriptions
    ADD COLUMN IF NOT EXISTS cancel_at_period_end BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS grace_until TIMESTAMPTZ NULL;

-- Hourly dunning sweep scans for subscriptions that have exited grace
-- without recovering payment. The partial index keeps the scan tight
-- (matches only the small set of currently-in-grace rows).
CREATE INDEX IF NOT EXISTS idx_subscriptions_grace_active
    ON public.subscriptions (grace_until)
    WHERE grace_until IS NOT NULL;

-- Companion index for cancel-at-period-end sweeps. Tiny set in practice
-- so a partial index is cheap and stays out of the way of the other
-- subscription queries.
CREATE INDEX IF NOT EXISTS idx_subscriptions_pending_cancel
    ON public.subscriptions (current_period_end)
    WHERE cancel_at_period_end = true;
