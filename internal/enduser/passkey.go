package enduser

// End-user passkeys (#630): passwordless WebAuthn sign-in for a project's own
// end users, the SDK counterpart of the console passkeys (internal/auth/
// passkey.go). Two things differ from the console:
//
//   - Per-project relying party. Each project declares its own rp_id +
//     origins (AuthConfig.Passkeys), so the *webauthn.WebAuthn is built per
//     request, not once at startup. Credentials are bound to the rp_id.
//   - Tenant-schema store. Credentials and ceremony state live in the
//     tenant's own user_passkey_credentials / webauthn_challenges tables
//     (migration 000137), reached as the service role via asService — the
//     INSERT/UPDATE RLS is service-only, so the server sets user_id and no
//     end user or SQLi can plant a credential against a victim.
//
// v1 is passwordless sign-in only (register + discoverable login). MFA-style
// "passkey required" enforcement for end users is a later follow-up (end-user
// JWTs carry no login_via today). Register challenges are stateful (the user
// is authenticated); the login begin is stateless (HMAC-signed, keyed off the
// project's JWT secret) so the unauthenticated endpoint writes nothing — the
// console's rationale.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/tenant"
)

const (
	passkeyChallengeTTL   = 5 * time.Minute
	enduserMaxPasskeys    = 10
	maxPasskeyNicknameLen = 64
)

// PasskeyUser is the end user resolved by a successful discoverable login —
// the caller (PR 3's handler) mints the end-user JWT from it.
type PasskeyUser struct {
	ID    string
	Email string
}

// buildWebAuthn constructs a per-project relying party from the project's
// passkey config. Same ceremony policy as the console (resident key +
// user-verification required = a single assertion is strong auth; no
// attestation).
func buildWebAuthn(cfg tenant.PasskeyConfig) (*webauthn.WebAuthn, error) {
	if cfg.RPID == "" || len(cfg.Origins) == 0 {
		return nil, auth.ErrPasskeysUnavailable
	}
	requireRK := true
	wa, err := webauthn.New(&webauthn.Config{
		RPID:          cfg.RPID,
		RPDisplayName: cfg.RPID,
		RPOrigins:     cfg.Origins,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			RequireResidentKey: &requireRK,
			ResidentKey:        protocol.ResidentKeyRequirementRequired,
			UserVerification:   protocol.VerificationRequired,
		},
		AttestationPreference: protocol.PreferNoAttestation,
		Timeouts: webauthn.TimeoutsConfig{
			Login:        webauthn.TimeoutConfig{Enforce: true, Timeout: passkeyChallengeTTL, TimeoutUVD: passkeyChallengeTTL},
			Registration: webauthn.TimeoutConfig{Enforce: true, Timeout: passkeyChallengeTTL, TimeoutUVD: passkeyChallengeTTL},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("webauthn config: %w", err)
	}
	return wa, nil
}

// ── webauthn.User adapter ──────────────────────────────────────────────

type waUser struct {
	id          []byte
	name        string
	displayName string
	creds       []webauthn.Credential
}

func (u *waUser) WebAuthnID() []byte                         { return u.id }
func (u *waUser) WebAuthnName() string                       { return u.name }
func (u *waUser) WebAuthnDisplayName() string                { return u.displayName }
func (u *waUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

// userHandle is the WebAuthn user.id: the 16 raw bytes of the tenant user's
// UUID — opaque and non-PII, as the spec asks.
func userHandle(userID string) ([]byte, error) {
	id, err := uuid.Parse(userID)
	if err != nil {
		return nil, fmt.Errorf("parse user id: %w", err)
	}
	b := id[:]
	return b, nil
}

// ── tenant-schema store (run inside an asService tx) ────────────────────

const passkeyCols = `id::text, credential_id, public_key, attestation_type, transports, aaguid, sign_count, nickname, created_at, last_used_at`

type storedCred struct {
	info auth.PasskeyInfo
	cred webauthn.Credential
}

func scanCred(row pgx.Row) (storedCred, error) {
	var sc storedCred
	var transports []string
	var signCount int64
	if err := row.Scan(&sc.info.ID, &sc.cred.ID, &sc.cred.PublicKey, &sc.cred.AttestationType,
		&transports, &sc.cred.Authenticator.AAGUID, &signCount,
		&sc.info.Nickname, &sc.info.CreatedAt, &sc.info.LastUsedAt); err != nil {
		return sc, err
	}
	sc.cred.Authenticator.SignCount = uint32(signCount)
	for _, t := range transports {
		sc.cred.Transport = append(sc.cred.Transport, protocol.AuthenticatorTransport(t))
	}
	sc.info.Transports = transports
	if sc.info.Transports == nil {
		sc.info.Transports = []string{}
	}
	return sc, nil
}

func listCreds(ctx context.Context, tx pgx.Tx, schema, userID string) ([]storedCred, error) {
	rows, err := tx.Query(ctx,
		`SELECT `+passkeyCols+` FROM `+quoteIdent(schema)+`.user_passkey_credentials
		  WHERE user_id = $1::uuid ORDER BY created_at, id`, userID)
	if err != nil {
		return nil, fmt.Errorf("list passkeys: %w", err)
	}
	defer rows.Close()
	var out []storedCred
	for rows.Next() {
		sc, err := scanCred(rows)
		if err != nil {
			return nil, fmt.Errorf("scan passkey: %w", err)
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

func insertCred(ctx context.Context, tx pgx.Tx, schema, userID string, cred *webauthn.Credential, nickname *string) (*auth.PasskeyInfo, error) {
	transports := make([]string, 0, len(cred.Transport))
	for _, t := range cred.Transport {
		transports = append(transports, string(t))
	}
	var aaguid []byte
	if len(cred.Authenticator.AAGUID) > 0 {
		aaguid = cred.Authenticator.AAGUID
	}
	sc, err := scanCred(tx.QueryRow(ctx,
		`INSERT INTO `+quoteIdent(schema)+`.user_passkey_credentials
		   (user_id, credential_id, public_key, attestation_type, transports, aaguid, sign_count, nickname)
		 VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8)
		 RETURNING `+passkeyCols,
		userID, cred.ID, cred.PublicKey, cred.AttestationType, transports, aaguid,
		int64(cred.Authenticator.SignCount), nickname))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, auth.ErrPasskeyAlreadyRegistered
		}
		return nil, fmt.Errorf("insert passkey: %w", err)
	}
	return &sc.info, nil
}

func recordUse(ctx context.Context, tx pgx.Tx, schema string, credentialID []byte, signCount uint32) error {
	_, err := tx.Exec(ctx,
		`UPDATE `+quoteIdent(schema)+`.user_passkey_credentials
		    SET sign_count = $2, last_used_at = now() WHERE credential_id = $1`,
		credentialID, int64(signCount))
	return err
}

func saveRegisterChallenge(ctx context.Context, tx pgx.Tx, schema, userID string, sd *webauthn.SessionData) (string, error) {
	data, err := json.Marshal(sd)
	if err != nil {
		return "", fmt.Errorf("marshal session data: %w", err)
	}
	// Opportunistic sweep of abandoned ceremonies (cheap; rows are short-lived).
	_, _ = tx.Exec(ctx, `DELETE FROM `+quoteIdent(schema)+`.webauthn_challenges WHERE expires_at < now() - interval '1 minute'`)
	var id string
	if err := tx.QueryRow(ctx,
		`INSERT INTO `+quoteIdent(schema)+`.webauthn_challenges (user_id, purpose, session_data, expires_at)
		 VALUES ($1::uuid, 'register', $2, now() + $3) RETURNING id::text`,
		userID, data, passkeyChallengeTTL).Scan(&id); err != nil {
		return "", fmt.Errorf("save challenge: %w", err)
	}
	return id, nil
}

func consumeRegisterChallenge(ctx context.Context, tx pgx.Tx, schema, id string) (string, *webauthn.SessionData, error) {
	if _, err := uuid.Parse(id); err != nil {
		return "", nil, auth.ErrPasskeyChallengeInvalid
	}
	var userID string
	var data []byte
	err := tx.QueryRow(ctx,
		`DELETE FROM `+quoteIdent(schema)+`.webauthn_challenges
		  WHERE id = $1::uuid AND purpose = 'register' AND expires_at > now()
		 RETURNING user_id::text, session_data`, id).Scan(&userID, &data)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, auth.ErrPasskeyChallengeInvalid
	}
	if err != nil {
		return "", nil, fmt.Errorf("consume challenge: %w", err)
	}
	var sd webauthn.SessionData
	if err := json.Unmarshal(data, &sd); err != nil {
		return "", nil, auth.ErrPasskeyChallengeInvalid
	}
	return userID, &sd, nil
}

// markLoginChallengeUsed records a stateless login-challenge id as consumed.
// Returns false if it was already used (replay lost the INSERT race).
func markLoginChallengeUsed(ctx context.Context, tx pgx.Tx, schema, id string, expires time.Time) (bool, error) {
	if _, err := uuid.Parse(id); err != nil {
		return false, auth.ErrPasskeyChallengeInvalid
	}
	// Opportunistic sweep: login markers are written on every sign-in, so
	// GC expired rows here too (not only on register-begin) — otherwise a
	// login-heavy project with rare new enrollments grows this table.
	_, _ = tx.Exec(ctx, `DELETE FROM `+quoteIdent(schema)+`.webauthn_challenges WHERE expires_at < now() - interval '1 minute'`)
	tag, err := tx.Exec(ctx,
		`INSERT INTO `+quoteIdent(schema)+`.webauthn_challenges (id, purpose, session_data, expires_at)
		 VALUES ($1::uuid, 'login', '{}'::jsonb, $2) ON CONFLICT (id) DO NOTHING`,
		id, expires)
	if err != nil {
		return false, fmt.Errorf("mark challenge used: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// ── stateless login challenge (HMAC-signed, keyed off the project JWT) ──

type loginChallenge struct {
	ID      string               `json:"id"`
	Session webauthn.SessionData `json:"sd"`
	Exp     int64                `json:"exp"`
}

func loginChallengeKey(jwtSecret []byte) []byte {
	m := hmac.New(sha256.New, jwtSecret)
	m.Write([]byte("eurobase/enduser-passkey-login-challenge/v1"))
	return m.Sum(nil)
}

func signLoginChallenge(jwtSecret []byte, sd *webauthn.SessionData) (string, error) {
	payload, err := json.Marshal(loginChallenge{ID: uuid.NewString(), Session: *sd, Exp: time.Now().Add(passkeyChallengeTTL).Unix()})
	if err != nil {
		return "", fmt.Errorf("marshal login challenge: %w", err)
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	m := hmac.New(sha256.New, loginChallengeKey(jwtSecret))
	m.Write([]byte(body))
	return body + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil)), nil
}

func verifyLoginChallenge(jwtSecret []byte, token string) (*loginChallenge, error) {
	body, sig, ok := cut(token, '.')
	if !ok {
		return nil, auth.ErrPasskeyChallengeInvalid
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return nil, auth.ErrPasskeyChallengeInvalid
	}
	m := hmac.New(sha256.New, loginChallengeKey(jwtSecret))
	m.Write([]byte(body))
	if !hmac.Equal(got, m.Sum(nil)) {
		return nil, auth.ErrPasskeyChallengeInvalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return nil, auth.ErrPasskeyChallengeInvalid
	}
	var lc loginChallenge
	if err := json.Unmarshal(payload, &lc); err != nil {
		return nil, auth.ErrPasskeyChallengeInvalid
	}
	if time.Now().Unix() > lc.Exp {
		return nil, auth.ErrPasskeyChallengeInvalid
	}
	return &lc, nil
}

func cut(s string, sep byte) (before, after string, found bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}

// ── ceremonies ──────────────────────────────────────────────────────────

// BeginPasskeyRegistration starts enrolling a passkey for an authenticated
// end user. config is the caller's project AuthConfig.
func (s *AuthService) BeginPasskeyRegistration(ctx context.Context, schema string, config tenant.AuthConfig, userID, userEmail string) (*auth.PasskeyChallenge, error) {
	if !config.IsPasskeyEnabled() {
		return nil, auth.ErrPasskeysUnavailable
	}
	wa, err := buildWebAuthn(*config.Passkeys)
	if err != nil {
		return nil, err
	}
	handle, err := userHandle(userID)
	if err != nil {
		return nil, err
	}

	var stored []storedCred
	if err := s.asService(ctx, func(ctx context.Context, tx pgx.Tx) error {
		stored, err = listCreds(ctx, tx, schema, userID)
		return err
	}); err != nil {
		return nil, err
	}
	if len(stored) >= enduserMaxPasskeys {
		return nil, auth.ErrPasskeyLimitReached
	}
	creds := make([]webauthn.Credential, 0, len(stored))
	for _, sc := range stored {
		creds = append(creds, sc.cred)
	}
	u := &waUser{id: handle, name: userEmail, displayName: userEmail, creds: creds}

	creation, sd, err := wa.BeginRegistration(u,
		webauthn.WithExclusions(webauthn.Credentials(creds).CredentialDescriptors()),
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
	)
	if err != nil {
		return nil, fmt.Errorf("begin registration: %w", err)
	}

	var challengeID string
	if err := s.asService(ctx, func(ctx context.Context, tx pgx.Tx) error {
		challengeID, err = saveRegisterChallenge(ctx, tx, schema, userID, sd)
		return err
	}); err != nil {
		return nil, err
	}
	opts, err := json.Marshal(creation)
	if err != nil {
		return nil, fmt.Errorf("marshal options: %w", err)
	}
	return &auth.PasskeyChallenge{ChallengeID: challengeID, Options: opts}, nil
}

// FinishPasskeyRegistration verifies the attestation and stores the credential.
func (s *AuthService) FinishPasskeyRegistration(ctx context.Context, schema string, config tenant.AuthConfig, userID string, challengeID string, credentialJSON []byte, nickname string) (*auth.PasskeyInfo, error) {
	if !config.IsPasskeyEnabled() {
		return nil, auth.ErrPasskeysUnavailable
	}
	wa, err := buildWebAuthn(*config.Passkeys)
	if err != nil {
		return nil, err
	}
	nick, err := normalizePasskeyNickname(nickname)
	if err != nil {
		return nil, err
	}
	handle, err := userHandle(userID)
	if err != nil {
		return nil, err
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(credentialJSON)
	if err != nil {
		slog.Info("enduser passkey register: parse failed", "error", err)
		return nil, auth.ErrPasskeyVerificationFailed
	}

	var info *auth.PasskeyInfo
	txErr := s.asService(ctx, func(ctx context.Context, tx pgx.Tx) error {
		chUser, sd, err := consumeRegisterChallenge(ctx, tx, schema, challengeID)
		if err != nil {
			return err
		}
		if chUser != userID {
			return auth.ErrPasskeyChallengeInvalid
		}
		stored, err := listCreds(ctx, tx, schema, userID)
		if err != nil {
			return err
		}
		if len(stored) >= enduserMaxPasskeys {
			return auth.ErrPasskeyLimitReached
		}
		creds := make([]webauthn.Credential, 0, len(stored))
		for _, sc := range stored {
			creds = append(creds, sc.cred)
		}
		u := &waUser{id: handle, name: userID, displayName: userID, creds: creds}
		cred, err := wa.CreateCredential(u, *sd, parsed)
		if err != nil {
			slog.Info("enduser passkey register: verification failed", "error", err)
			return auth.ErrPasskeyVerificationFailed
		}
		info, err = insertCred(ctx, tx, schema, userID, cred, nick)
		return err
	})
	if txErr != nil {
		return nil, txErr
	}
	slog.Info("enduser passkey registered", "schema", schema, "user_id", userID, "passkey_id", info.ID)
	return info, nil
}

// BeginPasskeyLogin starts a username-less (discoverable) sign-in. Stateless:
// the challenge is an HMAC-signed token (no DB write), single-use enforced at
// finish. jwtSecret is the project's JWT secret.
func (s *AuthService) BeginPasskeyLogin(ctx context.Context, config tenant.AuthConfig, jwtSecret []byte) (*auth.PasskeyChallenge, error) {
	if !config.IsPasskeyEnabled() {
		return nil, auth.ErrPasskeysUnavailable
	}
	wa, err := buildWebAuthn(*config.Passkeys)
	if err != nil {
		return nil, err
	}
	assertion, sd, err := wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return nil, fmt.Errorf("begin discoverable login: %w", err)
	}
	token, err := signLoginChallenge(jwtSecret, sd)
	if err != nil {
		return nil, err
	}
	opts, err := json.Marshal(assertion)
	if err != nil {
		return nil, fmt.Errorf("marshal options: %w", err)
	}
	return &auth.PasskeyChallenge{ChallengeID: token, Options: opts}, nil
}

// FinishPasskeyLogin verifies a discoverable assertion and returns the end
// user it belongs to (the caller mints the session). The whole verification
// runs in one service-role tx so the discoverable-login handler can load the
// user's credentials by the authenticator's user handle.
func (s *AuthService) FinishPasskeyLogin(ctx context.Context, schema string, config tenant.AuthConfig, jwtSecret []byte, challengeID string, credentialJSON []byte) (*PasskeyUser, error) {
	if !config.IsPasskeyEnabled() {
		return nil, auth.ErrPasskeysUnavailable
	}
	wa, err := buildWebAuthn(*config.Passkeys)
	if err != nil {
		return nil, err
	}
	lc, err := verifyLoginChallenge(jwtSecret, challengeID)
	if err != nil {
		return nil, err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(credentialJSON)
	if err != nil {
		slog.Info("enduser passkey login: parse failed", "error", err)
		return nil, auth.ErrPasskeyVerificationFailed
	}

	var out *PasskeyUser
	txErr := s.asService(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var resolvedID string
		handler := func(rawID, handle []byte) (webauthn.User, error) {
			id, err := uuid.FromBytes(handle)
			if err != nil {
				return nil, fmt.Errorf("invalid user handle")
			}
			stored, err := listCreds(ctx, tx, schema, id.String())
			if err != nil {
				return nil, err
			}
			creds := make([]webauthn.Credential, 0, len(stored))
			for _, sc := range stored {
				creds = append(creds, sc.cred)
				if bytes.Equal(sc.cred.ID, rawID) {
					resolvedID = id.String()
				}
			}
			return &waUser{id: handle, name: id.String(), displayName: id.String(), creds: creds}, nil
		}
		cred, err := wa.ValidateDiscoverableLogin(handler, lc.Session, parsed)
		if err == nil && cred.Authenticator.CloneWarning {
			err = errors.New("sign counter regression (possible cloned authenticator)")
		}
		if err != nil {
			slog.Warn("enduser passkey login failed", "schema", schema, "error", err)
			return auth.ErrPasskeyVerificationFailed
		}
		if resolvedID == "" {
			return auth.ErrPasskeyVerificationFailed
		}
		// Single use: record the challenge id now the assertion verified.
		fresh, err := markLoginChallengeUsed(ctx, tx, schema, lc.ID, time.Unix(lc.Exp, 0))
		if err != nil {
			return err
		}
		if !fresh {
			return auth.ErrPasskeyChallengeInvalid
		}
		if err := recordUse(ctx, tx, schema, cred.ID, cred.Authenticator.SignCount); err != nil {
			return err
		}
		// Load the user's email + ensure the account is usable.
		var email string
		var banned *time.Time
		if err := tx.QueryRow(ctx,
			`SELECT email, banned_at FROM `+quoteIdent(schema)+`.users WHERE id = $1::uuid`, resolvedID).
			Scan(&email, &banned); err != nil {
			return auth.ErrPasskeyVerificationFailed
		}
		if banned != nil {
			return auth.ErrPasskeyVerificationFailed
		}
		out = &PasskeyUser{ID: resolvedID, Email: email}
		return nil
	})
	if txErr != nil {
		return nil, txErr
	}
	slog.Info("enduser passkey login", "schema", schema, "user_id", out.ID)
	return out, nil
}

// ListPasskeys returns an authenticated end user's enrolled passkeys.
func (s *AuthService) ListPasskeys(ctx context.Context, schema, userID string) ([]auth.PasskeyInfo, error) {
	var out []auth.PasskeyInfo
	err := s.asService(ctx, func(ctx context.Context, tx pgx.Tx) error {
		stored, err := listCreds(ctx, tx, schema, userID)
		if err != nil {
			return err
		}
		out = make([]auth.PasskeyInfo, 0, len(stored))
		for _, sc := range stored {
			out = append(out, sc.info)
		}
		return nil
	})
	return out, err
}

// DeletePasskey removes one of the user's passkeys.
func (s *AuthService) DeletePasskey(ctx context.Context, schema, userID, id string) error {
	if _, err := uuid.Parse(id); err != nil {
		return auth.ErrPasskeyNotFound
	}
	return s.asService(ctx, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`DELETE FROM `+quoteIdent(schema)+`.user_passkey_credentials
			  WHERE id = $2::uuid AND user_id = $1::uuid`, userID, id)
		if err != nil {
			return fmt.Errorf("delete passkey: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return auth.ErrPasskeyNotFound
		}
		return nil
	})
}

func normalizePasskeyNickname(nickname string) (*string, error) {
	n := trimSpace(nickname)
	if n == "" {
		return nil, nil
	}
	if len([]rune(n)) > maxPasskeyNicknameLen {
		return nil, auth.ErrPasskeyNicknameInvalid
	}
	return &n, nil
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n' || s[start] == '\r') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}
