-- 000152_email_bounce_blocks.up.sql
--
-- Recipients whose mail from the platform's sender failed permanently,
-- for transactional mail (auth emails projects send through the platform
-- sender, and console account emails). Hard bounces count against the
-- sending domain's reputation; sending again to an address that just
-- bounced only adds to that. The send path skips an address while it is
-- blocked; the project's email log shows why.
--
-- Unlike email_suppressions (platform accounts, plain addresses), this
-- covers projects' end users too, so it keeps only a SHA-256 of the
-- lower-cased address and the kind of failure — never the address or the
-- provider's message (which can quote the address).
--
-- Filled hourly by the worker from the provider's failed-email list.
-- Platform table: the gateway role gets DML through the default
-- privileges set in 000037.

CREATE TABLE IF NOT EXISTS public.email_bounce_blocks (
    address_hash  TEXT        PRIMARY KEY CHECK (address_hash ~ '^[0-9a-f]{64}$'),
    kind          TEXT        NOT NULL CHECK (kind IN ('mailbox_not_found', 'mailbox_full', 'domain_not_found', 'blocklisted', 'hard_bounce')),
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    blocked_until TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS email_bounce_blocks_blocked_until_idx
    ON public.email_bounce_blocks (blocked_until);
