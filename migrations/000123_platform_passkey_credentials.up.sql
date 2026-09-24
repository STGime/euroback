-- 000123_platform_passkey_credentials.up.sql
--
-- Console MFA — passkey-first, all tiers, all users (#621).
--
-- Two tables:
--
--   * platform_passkey_credentials — one row per WebAuthn credential a
--     platform (console) user has enrolled. Multi-passkey per user
--     (laptop + phone + hardware key). A user with >= 1 row has MFA on:
--     password alone no longer mints a session, the password step is
--     followed by a passkey assertion (or the user signs in with the
--     passkey directly).
--
--   * platform_webauthn_challenges — single-use ceremony state.
--     'register' / 'step_up': stored at begin, consumed at finish with
--     DELETE … RETURNING, so a challenge works exactly once; for step_up
--     the row id doubles as the "password already verified" token.
--     'login' (username-less): begin is stateless (HMAC-signed token, so
--     the unauthenticated endpoint writes nothing); a row is INSERTed
--     only when an assertion verifies, as the used-marker that makes a
--     replay of the same token fail (ON CONFLICT on id).
--
-- Grants: both tables are platform auth config, never SDK-facing. Same
-- #443-class pitfall as organizations / support_requests: 000037's
-- ALTER DEFAULT PRIVILEGES auto-grants eurobase_gateway full DML on
-- every new migrator-owned public.* table. Write access from the gateway
-- pool would let a runtime SQLi INSERT an attacker-controlled public key
-- for any user = account takeover, so REVOKE from gateway and grant the
-- developer role only (PlatformAuthService uses its developer pool for
-- every query against these tables).
--
-- No explicit BEGIN/COMMIT: golang-migrate wraps each .up.sql in its own tx.

CREATE TABLE public.platform_passkey_credentials (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    platform_user_id   UUID NOT NULL REFERENCES public.platform_users(id) ON DELETE CASCADE,
    -- WebAuthn credential id (rawId). Globally unique per the spec;
    -- the UNIQUE index is also the lookup path for discoverable login.
    credential_id      BYTEA NOT NULL,
    -- COSE-encoded credential public key.
    public_key         BYTEA NOT NULL,
    attestation_type   TEXT NOT NULL DEFAULT '',
    attestation_format TEXT NOT NULL DEFAULT '',
    transports         TEXT[] NOT NULL DEFAULT '{}',
    -- Raw authenticator-data flags byte (UP/UV/BE/BS) as last seen.
    flags              SMALLINT NOT NULL DEFAULT 0,
    aaguid             BYTEA,
    sign_count         BIGINT NOT NULL DEFAULT 0,
    nickname           TEXT,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at       TIMESTAMPTZ,

    CHECK (octet_length(credential_id) BETWEEN 1 AND 1023),
    CHECK (nickname IS NULL OR char_length(nickname) BETWEEN 1 AND 64)
);

CREATE UNIQUE INDEX ux_platform_passkey_credentials_credential_id
    ON public.platform_passkey_credentials (credential_id);
CREATE INDEX ix_platform_passkey_credentials_user
    ON public.platform_passkey_credentials (platform_user_id, created_at);

CREATE TABLE public.platform_webauthn_challenges (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- NULL for a discoverable (username-less) login: the user is only
    -- known once the authenticator returns its userHandle.
    platform_user_id UUID REFERENCES public.platform_users(id) ON DELETE CASCADE,
    purpose          TEXT NOT NULL,
    -- Serialized go-webauthn SessionData (challenge, allowed creds, UV).
    session_data     JSONB NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at       TIMESTAMPTZ NOT NULL,

    CHECK (purpose IN ('register', 'login', 'step_up')),
    CHECK (purpose = 'login' OR platform_user_id IS NOT NULL)
);

-- Opportunistic expiry sweep on every insert filters on expires_at.
CREATE INDEX ix_platform_webauthn_challenges_expires
    ON public.platform_webauthn_challenges (expires_at);

-- ── Grants ─────────────────────────────────────────────────────────
REVOKE ALL ON public.platform_passkey_credentials FROM eurobase_gateway;
REVOKE ALL ON public.platform_webauthn_challenges FROM eurobase_gateway;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.platform_passkey_credentials TO eurobase_developer;
GRANT SELECT, INSERT, DELETE ON public.platform_webauthn_challenges TO eurobase_developer;

-- Password reset on a passkey account updates platform_users.password_hash
-- and deletes the passkeys in ONE transaction on the developer pool (so a
-- partial failure can't strip MFA while leaving the old password). Grant
-- the exact columns explicitly rather than relying on eurobase_developer's
-- INHERIT membership in the owning migrator role — prod managed-PG role
-- option defaults differ from local PG (see scripts/ops notes).
GRANT SELECT (id, email), UPDATE (password_hash) ON public.platform_users TO eurobase_developer;

COMMENT ON TABLE public.platform_passkey_credentials IS
  'WebAuthn / passkey credentials for console (platform) users (#621). >= 1 row = MFA on for that user. Developer pool only.';
COMMENT ON TABLE public.platform_webauthn_challenges IS
  'Single-use WebAuthn ceremony state: register / step_up rows consumed via DELETE ... RETURNING; login rows are used-markers for stateless challenge tokens. Developer pool only.';
