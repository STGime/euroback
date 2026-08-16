-- 000103_subscriptions_mollie_payment_id.up.sql
--
-- HOTFIX for #406 / PR #407. activateNewProjectFromFirstPayment
-- correlates by mollie_payment_id because a new-project webhook
-- creates the subscription row itself — no pre-existing
-- subscription_id in metadata (unlike the upgrade-existing-project
-- path, which stores subscription_id at CreateCheckout time and
-- looks up by that).
--
-- The column existed on invoices (migration 000001, UNIQUE) but
-- was never added to subscriptions. Missing here surfaced in prod
-- on 2026-08-16 as SQLSTATE 42703 on the webhook's guard-1
-- idempotency check, blocking new-project activation entirely.
--
-- Nullable — only populated for subscriptions created via the
-- new-project webhook. Legacy upgrade-existing-project rows keep
-- NULL. Partial unique for the lookup + to catch duplicate
-- webhook fires; NULLs are ignored by the partial index, so
-- legacy rows are unaffected.

BEGIN;

ALTER TABLE public.subscriptions
    ADD COLUMN IF NOT EXISTS mollie_payment_id TEXT;

CREATE UNIQUE INDEX IF NOT EXISTS idx_subscriptions_mollie_payment
    ON public.subscriptions(mollie_payment_id)
    WHERE mollie_payment_id IS NOT NULL;

COMMIT;
