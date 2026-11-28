-- One row per recipient of a service notice (Admin → Service notice),
-- e.g. the email notice of a change to the Terms (§15). It records who
-- got which notice and the provider's message id, and makes a send
-- resumable: sending the same notice again (same subject) skips the
-- addresses already marked sent. Platform table; the gateway writes it.

CREATE TABLE IF NOT EXISTS public.service_notice_deliveries (
    notice_key  TEXT        NOT NULL CHECK (notice_key ~ '^[0-9a-f]{64}$'), -- SHA-256 of the trimmed subject
    subject     TEXT        NOT NULL,
    email       TEXT        NOT NULL,
    status      TEXT        NOT NULL CHECK (status IN ('sent', 'failed')),
    message_id  TEXT,
    error       TEXT,
    attempted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (notice_key, email)
);
