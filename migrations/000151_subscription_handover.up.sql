-- 000151_subscription_handover.up.sql
--
-- Billing handover on a project ownership transfer (#846): the new owner
-- can set up their own subscription while the previous owner's paid period
-- is still running, without paying for that period twice and without a
-- Free gap when it ends.
--
-- A 'scheduled' subscription is the new owner's: its first payment is a
-- zero-amount mandate payment (card / PayPal), and its Mollie subscription
-- starts at starts_at — the previous subscription's next_charge_at. When
-- that date passes, the downgrade sweep (or the first recurring webhook)
-- expires the previous subscription and makes this one 'active'.
--
-- 'scheduled' is deliberately outside idx_subscriptions_project_live, so it
-- can exist next to the live subscription it follows; its own partial
-- unique index allows one per project.

ALTER TABLE public.subscriptions
    DROP CONSTRAINT IF EXISTS subscriptions_status_check;

ALTER TABLE public.subscriptions
    ADD CONSTRAINT subscriptions_status_check
        CHECK (status IN ('incomplete', 'scheduled', 'active', 'past_due', 'canceled', 'expired'));

ALTER TABLE public.subscriptions
    ADD COLUMN IF NOT EXISTS starts_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS predecessor_id UUID REFERENCES public.subscriptions(id) ON DELETE SET NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_subscriptions_project_scheduled
    ON public.subscriptions (project_id)
    WHERE status = 'scheduled';
