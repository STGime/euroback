package auth

// Console passkey MFA (#621).
//
// Model: a passkey assertion with userVerification=required is MFA on
// its own (possession of the authenticator + biometric / device PIN),
// so it can mint a session directly. A user with >= 1 enrolled passkey
// has MFA on: password alone no longer mints a session — SignIn returns
// an MFA-pending step-up challenge that the console completes with a
// passkey assertion. Both paths mint login_via=passkey.
//
// login_via=passkey is deliberately NOT accepted by
// organizations.sso_required: SessionSatisfiesSSOFor only accepts
// login_via=sso for the matching org, so a passkey session reaches the
// user's personal projects but not SSO-required org projects — the same
// per-resource behaviour password sessions have today.
//
// Ceremony state (the WebAuthn challenge) is stored server-side in
// public.platform_webauthn_challenges and consumed with DELETE …
// RETURNING, so each challenge is single-use. Both passkey tables are
// REVOKEd from eurobase_gateway (migration 000123); every query here
// runs on the developer pool.

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
	"net/url"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/eurobase/euroback/internal/audit"
)

const (
	// Purposes stored in platform_webauthn_challenges.purpose.
	challengePurposeRegister = "register"
	challengePurposeLogin    = "login"
	challengePurposeStepUp   = "step_up"

	// challengeTTL bounds how long a begin → finish round trip may
	// take. Matches go-webauthn's default ceremony timeout.
	challengeTTL = 5 * time.Minute

	// maxPasskeysPerUser caps enrolment. Laptop + phone + a couple of
	// hardware keys fits comfortably; the cap keeps allowCredentials
	// lists and the account page bounded.
	maxPasskeysPerUser = 10

	// passkeyReauthWindow: a session minted this recently may add or
	// remove passkeys without re-entering the password. Older sessions
	// must confirm the password, so a stolen long-lived session can't
	// silently enrol an attacker's passkey (which would outlive the
	// JWT and bypass the password entirely).
	passkeyReauthWindow = 10 * time.Minute

	maxPasskeyNicknameLen = 64
)

var (
	// ErrPasskeysUnavailable is returned when passkeys are not wired
	// (no developer pool / WebAuthn config — dev or test). HTTP → 503.
	ErrPasskeysUnavailable = errors.New("passkeys are not available")
	// ErrPasskeyChallengeInvalid: unknown, expired, already-used, or
	// wrong-purpose challenge. HTTP → 400, console restarts the flow.
	ErrPasskeyChallengeInvalid = errors.New("passkey challenge is invalid or has expired — please try again")
	// ErrPasskeyVerificationFailed covers every assertion / attestation
	// verification failure. Deliberately generic: the detailed reason
	// goes to the log + audit, not to the caller.
	ErrPasskeyVerificationFailed = errors.New("passkey verification failed")
	ErrPasskeyNotFound           = errors.New("passkey not found")
	ErrPasskeyAlreadyRegistered  = errors.New("this passkey is already registered")
	ErrPasskeyLimitReached       = fmt.Errorf("you can register at most %d passkeys", maxPasskeysPerUser)
	ErrPasskeyNicknameInvalid    = fmt.Errorf("nickname must be 1-%d characters", maxPasskeyNicknameLen)
	// ErrReauthRequired: the session is older than passkeyReauthWindow
	// and no (or a wrong) current password was supplied. HTTP → 403
	// with code "reauth_required".
	ErrReauthRequired = errors.New("please confirm your current password to manage passkeys")
)

// PasskeyConfig is the WebAuthn relying-party configuration.
type PasskeyConfig struct {
	// RPID is the WebAuthn relying-party id: the console host
	// (console.eurobase.app), NOT the registrable suffix eurobase.app —
	// tenant apps are served from *.eurobase.app subdomains and must
	// not share the console's passkey scope.
	RPID          string
	RPDisplayName string
	// RPOrigins are the exact origins allowed in clientDataJSON
	// (https://console.eurobase.app).
	RPOrigins []string
}

// PasskeyConfigFromConsoleURL derives the RP config from the console's
// public URL. rpIDOverride / originsOverride (comma-separated) win when
// set — WEBAUTHN_RP_ID / WEBAUTHN_RP_ORIGINS.
func PasskeyConfigFromConsoleURL(consoleURL, rpIDOverride, originsOverride string) (PasskeyConfig, error) {
	cfg := PasskeyConfig{RPDisplayName: "Eurobase Console"}
	if originsOverride != "" {
		for _, o := range strings.Split(originsOverride, ",") {
			if o = strings.TrimRight(strings.TrimSpace(o), "/"); o != "" {
				cfg.RPOrigins = append(cfg.RPOrigins, o)
			}
		}
	} else if consoleURL != "" {
		u, err := url.Parse(consoleURL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return cfg, fmt.Errorf("invalid console URL %q", consoleURL)
		}
		cfg.RPOrigins = []string{u.Scheme + "://" + u.Host}
	}
	cfg.RPID = rpIDOverride
	if cfg.RPID == "" && len(cfg.RPOrigins) > 0 {
		u, err := url.Parse(cfg.RPOrigins[0])
		if err != nil {
			return cfg, fmt.Errorf("invalid origin %q", cfg.RPOrigins[0])
		}
		cfg.RPID = u.Hostname()
	}
	if cfg.RPID == "" || len(cfg.RPOrigins) == 0 {
		return cfg, errors.New("passkey RP id / origins could not be derived")
	}
	return cfg, nil
}

// PasskeyInfo is the console-facing view of an enrolled passkey. Never
// exposes key material.
type PasskeyInfo struct {
	ID         string     `json:"id"`
	Nickname   *string    `json:"nickname"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	// BackedUp: the authenticator reports the credential as synced
	// (iCloud Keychain, Google Password Manager, 1Password …) — lets
	// the console label "synced passkey" vs "device-bound".
	BackedUp   bool     `json:"backed_up"`
	Transports []string `json:"transports"`
}

// PasskeyChallenge is returned by every *Begin call. Options is the
// WebAuthn options object ({"publicKey": {...}}) the console hands to
// navigator.credentials.create / .get.
type PasskeyChallenge struct {
	ChallengeID string          `json:"challenge_id"`
	Options     json.RawMessage `json:"options"`
}

// PasskeyRequestMeta carries request context for audit entries.
type PasskeyRequestMeta struct {
	IP        string
	UserAgent string
}

// storedPasskey is one platform_passkey_credentials row.
type storedPasskey struct {
	Info       PasskeyInfo
	Credential webauthn.Credential
}

// passkeyStore abstracts the two passkey tables so the ceremony logic
// is unit-testable without Postgres. pgPasskeyStore is the real one.
type passkeyStore interface {
	ListCredentials(ctx context.Context, userID string) ([]storedPasskey, error)
	CountCredentials(ctx context.Context, userID string) (int, error)
	InsertCredential(ctx context.Context, userID string, cred *webauthn.Credential, nickname *string) (*PasskeyInfo, error)
	RecordUse(ctx context.Context, credentialID []byte, signCount uint32, flags protocol.AuthenticatorFlags) error
	RenameCredential(ctx context.Context, userID, id string, nickname *string) error
	DeleteCredential(ctx context.Context, userID, id string) error
	SaveChallenge(ctx context.Context, userID string, purpose string, sd *webauthn.SessionData) (string, error)
	// ConsumeChallenge atomically deletes and returns an unexpired
	// register / step_up challenge of the given purpose.
	ConsumeChallenge(ctx context.Context, id, purpose string) (userID string, sd *webauthn.SessionData, err error)
	// MarkChallengeUsed records a stateless login challenge id as
	// consumed. false = already used (replay).
	MarkChallengeUsed(ctx context.Context, id string, expires time.Time) (bool, error)
	// ResetPasswordClearingPasskeys updates platform_users.password_hash
	// and deletes all the user's passkeys atomically.
	ResetPasswordClearingPasskeys(ctx context.Context, userID, passwordHash string) (email string, cleared int, err error)
}

// passkeyIdentity is the platform_users data a passkey session needs.
type passkeyIdentity struct {
	User           PlatformUser
	IsSuperadmin   bool
	EmailConfirmed bool
}

// auditLogger is the slice of *audit.Service passkeys use; an
// interface so tests can capture entries.
type auditLogger interface {
	Log(ctx context.Context, projectID, actorID, actorEmail, action string, opts ...audit.LogOption)
}

// passkeyNoticeEmailer is implemented by *email.EmailService. Optional
// (type-asserted) so the PlatformEmailer interface — and its test
// fakes — don't grow.
type passkeyNoticeEmailer interface {
	SendPlatformPasskeysClearedEmail(ctx context.Context, userEmail string) error
}

// EnablePasskeys wires WebAuthn. Requires the developer pool (both
// passkey tables are developer-only). Call after SetDeveloperPool.
func (s *PlatformAuthService) EnablePasskeys(cfg PasskeyConfig) error {
	if s.developerPool == nil {
		return errors.New("passkeys require the developer pool")
	}
	return s.enablePasskeysWithStore(cfg, &pgPasskeyStore{pool: s.developerPool}, audit.NewService(s.pool))
}

func (s *PlatformAuthService) enablePasskeysWithStore(cfg PasskeyConfig, store passkeyStore, auditor auditLogger) error {
	requireRK := true
	wa, err := webauthn.New(&webauthn.Config{
		RPID:          cfg.RPID,
		RPDisplayName: cfg.RPDisplayName,
		RPOrigins:     cfg.RPOrigins,
		// Passkeys are discoverable credentials with user verification;
		// UV=required is what makes a single assertion count as MFA.
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			RequireResidentKey: &requireRK,
			ResidentKey:        protocol.ResidentKeyRequirementRequired,
			UserVerification:   protocol.VerificationRequired,
		},
		// No attestation: we don't restrict authenticator makes, and
		// "none" avoids a device-identifying attestation statement.
		AttestationPreference: protocol.PreferNoAttestation,
		Timeouts: webauthn.TimeoutsConfig{
			Login:        webauthn.TimeoutConfig{Enforce: true, Timeout: challengeTTL, TimeoutUVD: challengeTTL},
			Registration: webauthn.TimeoutConfig{Enforce: true, Timeout: challengeTTL, TimeoutUVD: challengeTTL},
		},
	})
	if err != nil {
		return fmt.Errorf("webauthn config: %w", err)
	}
	s.webAuthn = wa
	s.passkeys = store
	s.auditor = auditor
	if s.lookupIdentity == nil {
		s.lookupIdentity = s.lookupIdentityFromPool
	}
	if s.checkPassword == nil {
		s.checkPassword = s.passwordMatches
	}
	return nil
}

func (s *PlatformAuthService) passkeysEnabled() bool {
	return s.webAuthn != nil && s.passkeys != nil
}

// ── webauthn.User adapter ──────────────────────────────────────────

type webauthnUser struct {
	id          []byte
	name        string
	displayName string
	creds       []webauthn.Credential
}

func (u *webauthnUser) WebAuthnID() []byte                         { return u.id }
func (u *webauthnUser) WebAuthnName() string                       { return u.name }
func (u *webauthnUser) WebAuthnDisplayName() string                { return u.displayName }
func (u *webauthnUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

// userHandle is the WebAuthn user.id: the 16 raw bytes of the
// platform_users UUID. Opaque and non-PII, as the spec asks.
func userHandle(userID string) ([]byte, error) {
	id, err := uuid.Parse(userID)
	if err != nil {
		return nil, fmt.Errorf("parse user id: %w", err)
	}
	b := id[:]
	return b, nil
}

func (s *PlatformAuthService) loadWebAuthnUser(ctx context.Context, userID string) (*webauthnUser, *passkeyIdentity, []storedPasskey, error) {
	ident, err := s.lookupIdentity(ctx, userID)
	if err != nil {
		return nil, nil, nil, err
	}
	stored, err := s.passkeys.ListCredentials(ctx, userID)
	if err != nil {
		return nil, nil, nil, err
	}
	handle, err := userHandle(userID)
	if err != nil {
		return nil, nil, nil, err
	}
	u := &webauthnUser{id: handle, name: ident.User.Email, displayName: ident.User.Email}
	if ident.User.DisplayName != nil && *ident.User.DisplayName != "" {
		u.displayName = *ident.User.DisplayName
	}
	for _, sp := range stored {
		u.creds = append(u.creds, sp.Credential)
	}
	return u, ident, stored, nil
}

func (s *PlatformAuthService) lookupIdentityFromPool(ctx context.Context, userID string) (*passkeyIdentity, error) {
	var ident passkeyIdentity
	var confirmedAt *time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT id, email, display_name, COALESCE(is_superadmin, false), email_confirmed_at
		   FROM platform_users WHERE id = $1`,
		userID,
	).Scan(&ident.User.ID, &ident.User.Email, &ident.User.DisplayName, &ident.IsSuperadmin, &confirmedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("user not found")
		}
		return nil, fmt.Errorf("query user: %w", err)
	}
	ident.EmailConfirmed = confirmedAt != nil
	return &ident, nil
}

func (s *PlatformAuthService) audit(ctx context.Context, actorID, actorEmail, action string, meta PasskeyRequestMeta, metadata map[string]interface{}) {
	if s.auditor == nil {
		return
	}
	if meta.UserAgent != "" {
		if metadata == nil {
			metadata = map[string]interface{}{}
		}
		metadata["user_agent"] = meta.UserAgent
	}
	s.auditor.Log(ctx, "", actorID, actorEmail, action, audit.WithMetadata(metadata), audit.WithIP(meta.IP))
}

func marshalOptions(v interface{}) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal webauthn options: %w", err)
	}
	return b, nil
}

// ── Enrolment ──────────────────────────────────────────────────────

// CheckPasskeyReauth enforces the re-auth rule for adding / removing a
// passkey: either the session is fresh (issued within
// passkeyReauthWindow) or currentPassword matches.
//
// SSO sessions never get the fresh-session exemption: whoever controls
// an org's IdP can mint an SSO session for a member, and must not be
// able to enrol their own passkey on (or strip MFA from) that member's
// personal account with it.
func (s *PlatformAuthService) CheckPasskeyReauth(ctx context.Context, claims *Claims, currentPassword string) error {
	fresh := !claims.IssuedAt.IsZero() && time.Since(claims.IssuedAt) < passkeyReauthWindow
	if fresh && claims.LoginVia != LoginViaSSO {
		return nil
	}
	if currentPassword == "" {
		return ErrReauthRequired
	}
	if s.checkPassword == nil {
		return ErrReauthRequired
	}
	ok, err := s.checkPassword(ctx, claims.Subject, currentPassword)
	if err != nil {
		return err
	}
	if !ok {
		return ErrReauthRequired
	}
	return nil
}

// BeginPasskeyRegistration starts enrolling a new passkey for userID.
func (s *PlatformAuthService) BeginPasskeyRegistration(ctx context.Context, userID string) (*PasskeyChallenge, error) {
	if !s.passkeysEnabled() {
		return nil, ErrPasskeysUnavailable
	}
	u, _, stored, err := s.loadWebAuthnUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if len(stored) >= maxPasskeysPerUser {
		return nil, ErrPasskeyLimitReached
	}
	creation, sd, err := s.webAuthn.BeginRegistration(u,
		// Don't let the same authenticator enrol twice.
		webauthn.WithExclusions(webauthn.Credentials(u.creds).CredentialDescriptors()),
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
	)
	if err != nil {
		return nil, fmt.Errorf("begin registration: %w", err)
	}
	id, err := s.passkeys.SaveChallenge(ctx, userID, challengePurposeRegister, sd)
	if err != nil {
		return nil, err
	}
	opts, err := marshalOptions(creation)
	if err != nil {
		return nil, err
	}
	return &PasskeyChallenge{ChallengeID: id, Options: opts}, nil
}

// FinishPasskeyRegistration verifies the attestation response and
// stores the credential.
func (s *PlatformAuthService) FinishPasskeyRegistration(ctx context.Context, userID, challengeID string, credentialJSON []byte, nickname string, meta PasskeyRequestMeta) (*PasskeyInfo, error) {
	if !s.passkeysEnabled() {
		return nil, ErrPasskeysUnavailable
	}
	nick, err := normalizeNickname(nickname)
	if err != nil {
		return nil, err
	}
	chUser, sd, err := s.passkeys.ConsumeChallenge(ctx, challengeID, challengePurposeRegister)
	if err != nil {
		return nil, err
	}
	if chUser != userID {
		return nil, ErrPasskeyChallengeInvalid
	}
	u, ident, stored, err := s.loadWebAuthnUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if len(stored) >= maxPasskeysPerUser {
		return nil, ErrPasskeyLimitReached
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(credentialJSON)
	if err != nil {
		slog.Info("passkey registration: parse failed", "user_id", userID, "error", err)
		return nil, ErrPasskeyVerificationFailed
	}
	cred, err := s.webAuthn.CreateCredential(u, *sd, parsed)
	if err != nil {
		slog.Info("passkey registration: verification failed", "user_id", userID, "error", describeWebAuthnErr(err))
		return nil, ErrPasskeyVerificationFailed
	}
	info, err := s.passkeys.InsertCredential(ctx, userID, cred, nick)
	if err != nil {
		return nil, err
	}
	slog.Info("passkey registered", "user_id", userID, "passkey_id", info.ID, "passkey_count", len(stored)+1)
	s.audit(ctx, userID, ident.User.Email, audit.ActionPasskeyRegistered, meta, map[string]interface{}{
		"passkey_id":    info.ID,
		"backed_up":     info.BackedUp,
		"passkey_count": len(stored) + 1,
	})
	return info, nil
}

// ListPasskeys returns the user's enrolled passkeys, oldest first.
func (s *PlatformAuthService) ListPasskeys(ctx context.Context, userID string) ([]PasskeyInfo, error) {
	if !s.passkeysEnabled() {
		return nil, ErrPasskeysUnavailable
	}
	stored, err := s.passkeys.ListCredentials(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]PasskeyInfo, 0, len(stored))
	for _, sp := range stored {
		out = append(out, sp.Info)
	}
	return out, nil
}

// RenamePasskey sets (or clears, with "") a passkey's nickname.
func (s *PlatformAuthService) RenamePasskey(ctx context.Context, userID, id, nickname string) error {
	if !s.passkeysEnabled() {
		return ErrPasskeysUnavailable
	}
	nick, err := normalizeNickname(nickname)
	if err != nil {
		return err
	}
	return s.passkeys.RenameCredential(ctx, userID, id, nick)
}

// DeletePasskey removes one passkey. Deleting the last one turns MFA
// off for the account (password alone signs in again).
func (s *PlatformAuthService) DeletePasskey(ctx context.Context, userID, userEmail, id string, meta PasskeyRequestMeta) error {
	if !s.passkeysEnabled() {
		return ErrPasskeysUnavailable
	}
	if err := s.passkeys.DeleteCredential(ctx, userID, id); err != nil {
		return err
	}
	remaining, err := s.passkeys.CountCredentials(ctx, userID)
	if err != nil {
		remaining = -1
	}
	slog.Info("passkey removed", "user_id", userID, "passkey_id", id, "remaining", remaining)
	s.audit(ctx, userID, userEmail, audit.ActionPasskeyRemoved, meta, map[string]interface{}{
		"passkey_id":       id,
		"remaining":        remaining,
		"mfa_now_disabled": remaining == 0,
	})
	return nil
}

// ── Sign-in ────────────────────────────────────────────────────────

// BeginPasskeyLogin starts a username-less (discoverable) sign-in: the
// browser offers every passkey it holds for this RP.
//
// Stateless: the challenge travels to the client as an HMAC-signed
// token (see signLoginChallenge) instead of a DB row, so this
// unauthenticated endpoint writes nothing and needs no rate limit — a
// per-IP limit would be product-wide behind the LB (see
// internal/ratelimit/auth.go) and let one client lock everyone out of
// passkey sign-in. Single use is enforced at finish, where a verified
// assertion records the challenge id (MarkChallengeUsed).
func (s *PlatformAuthService) BeginPasskeyLogin(ctx context.Context) (*PasskeyChallenge, error) {
	if !s.passkeysEnabled() {
		return nil, ErrPasskeysUnavailable
	}
	assertion, sd, err := s.webAuthn.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return nil, fmt.Errorf("begin discoverable login: %w", err)
	}
	token, err := s.signLoginChallenge(sd)
	if err != nil {
		return nil, err
	}
	opts, err := marshalOptions(assertion)
	if err != nil {
		return nil, err
	}
	return &PasskeyChallenge{ChallengeID: token, Options: opts}, nil
}

// loginChallenge is the signed payload of a discoverable-login token.
type loginChallenge struct {
	ID      string               `json:"id"`
	Session webauthn.SessionData `json:"sd"`
	Exp     int64                `json:"exp"`
}

// loginChallengeKey derives a dedicated HMAC key from the platform JWT
// secret (domain-separated, so a challenge token can never verify as a
// JWT or vice versa).
func (s *PlatformAuthService) loginChallengeKey() []byte {
	m := hmac.New(sha256.New, s.jwtSecret)
	m.Write([]byte("eurobase/passkey-login-challenge/v1"))
	return m.Sum(nil)
}

func (s *PlatformAuthService) signLoginChallenge(sd *webauthn.SessionData) (string, error) {
	payload, err := json.Marshal(loginChallenge{
		ID:      uuid.NewString(),
		Session: *sd,
		Exp:     time.Now().Add(challengeTTL).Unix(),
	})
	if err != nil {
		return "", fmt.Errorf("marshal login challenge: %w", err)
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	m := hmac.New(sha256.New, s.loginChallengeKey())
	m.Write([]byte(body))
	return body + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil)), nil
}

func (s *PlatformAuthService) verifyLoginChallenge(token string) (*loginChallenge, error) {
	body, sig, ok := strings.Cut(token, ".")
	if !ok {
		return nil, ErrPasskeyChallengeInvalid
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return nil, ErrPasskeyChallengeInvalid
	}
	m := hmac.New(sha256.New, s.loginChallengeKey())
	m.Write([]byte(body))
	if !hmac.Equal(got, m.Sum(nil)) {
		return nil, ErrPasskeyChallengeInvalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return nil, ErrPasskeyChallengeInvalid
	}
	var lc loginChallenge
	if err := json.Unmarshal(payload, &lc); err != nil {
		return nil, ErrPasskeyChallengeInvalid
	}
	if time.Now().Unix() > lc.Exp {
		return nil, ErrPasskeyChallengeInvalid
	}
	return &lc, nil
}

// FinishPasskeyLogin verifies a discoverable assertion and mints a
// login_via=passkey session.
func (s *PlatformAuthService) FinishPasskeyLogin(ctx context.Context, challengeID string, credentialJSON []byte, meta PasskeyRequestMeta) (*PlatformAuthResponse, error) {
	if !s.passkeysEnabled() {
		return nil, ErrPasskeysUnavailable
	}
	lc, err := s.verifyLoginChallenge(challengeID)
	if err != nil {
		return nil, err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(credentialJSON)
	if err != nil {
		slog.Info("passkey login: parse failed", "error", err)
		return nil, ErrPasskeyVerificationFailed
	}

	// Resolve the user from the authenticator's userHandle. Captured
	// for the failure audit — but only when the presented credential
	// really belongs to that user: the userHandle is caller-chosen, so
	// otherwise anyone knowing a user's UUID could spam "sign-in
	// failed" entries onto their account.
	var ident *passkeyIdentity
	handler := func(rawID, handle []byte) (webauthn.User, error) {
		id, err := uuid.FromBytes(handle)
		if err != nil {
			return nil, fmt.Errorf("invalid user handle")
		}
		u, idn, _, err := s.loadWebAuthnUser(ctx, id.String())
		if err != nil {
			return nil, err
		}
		for _, c := range u.creds {
			if bytes.Equal(c.ID, rawID) {
				ident = idn
				break
			}
		}
		return u, nil
	}
	cred, err := s.webAuthn.ValidateDiscoverableLogin(handler, lc.Session, parsed)
	if err == nil && cred.Authenticator.CloneWarning {
		err = errors.New("sign counter regression (possible cloned authenticator)")
	}
	if err != nil {
		slog.Warn("passkey login failed", "error", describeWebAuthnErr(err))
		if ident != nil {
			s.audit(ctx, ident.User.ID, ident.User.Email, audit.ActionPasskeySignInFailed, meta, map[string]interface{}{
				"mode":   "passkey",
				"reason": describeWebAuthnErr(err),
			})
		}
		return nil, ErrPasskeyVerificationFailed
	}
	// Single use: record the challenge id now that the assertion
	// verified. A replay of the same token + assertion loses the
	// INSERT race and is refused.
	fresh, err := s.passkeys.MarkChallengeUsed(ctx, lc.ID, time.Unix(lc.Exp, 0))
	if err != nil {
		return nil, err
	}
	if !fresh {
		return nil, ErrPasskeyChallengeInvalid
	}
	if !ident.EmailConfirmed {
		return nil, ErrEmailNotVerified
	}
	return s.completePasskeySession(ctx, ident, cred, "passkey", meta)
}

// beginStepUp is called by SignIn after a correct password for a user
// who has passkeys. The returned challenge id is the MFA token: it
// proves the password step passed, is bound to the user, and is
// single-use.
func (s *PlatformAuthService) beginStepUp(ctx context.Context, userID string) (*PasskeyChallenge, error) {
	u, _, _, err := s.loadWebAuthnUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	assertion, sd, err := s.webAuthn.BeginLogin(u, webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return nil, fmt.Errorf("begin step-up: %w", err)
	}
	id, err := s.passkeys.SaveChallenge(ctx, userID, challengePurposeStepUp, sd)
	if err != nil {
		return nil, err
	}
	opts, err := marshalOptions(assertion)
	if err != nil {
		return nil, err
	}
	return &PasskeyChallenge{ChallengeID: id, Options: opts}, nil
}

// FinishStepUp completes password → passkey sign-in.
func (s *PlatformAuthService) FinishStepUp(ctx context.Context, mfaToken string, credentialJSON []byte, meta PasskeyRequestMeta) (*PlatformAuthResponse, error) {
	if !s.passkeysEnabled() {
		return nil, ErrPasskeysUnavailable
	}
	userID, sd, err := s.passkeys.ConsumeChallenge(ctx, mfaToken, challengePurposeStepUp)
	if err != nil {
		return nil, err
	}
	u, ident, _, err := s.loadWebAuthnUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(credentialJSON)
	if err != nil {
		slog.Info("passkey step-up: parse failed", "user_id", userID, "error", err)
		return nil, ErrPasskeyVerificationFailed
	}
	cred, err := s.webAuthn.ValidateLogin(u, *sd, parsed)
	if err == nil && cred.Authenticator.CloneWarning {
		err = errors.New("sign counter regression (possible cloned authenticator)")
	}
	if err != nil {
		slog.Warn("passkey step-up failed", "user_id", userID, "error", describeWebAuthnErr(err))
		s.audit(ctx, ident.User.ID, ident.User.Email, audit.ActionPasskeySignInFailed, meta, map[string]interface{}{
			"mode":   "password_step_up",
			"reason": describeWebAuthnErr(err),
		})
		return nil, ErrPasskeyVerificationFailed
	}
	return s.completePasskeySession(ctx, ident, cred, "password_step_up", meta)
}

func (s *PlatformAuthService) completePasskeySession(ctx context.Context, ident *passkeyIdentity, cred *webauthn.Credential, mode string, meta PasskeyRequestMeta) (*PlatformAuthResponse, error) {
	if err := s.passkeys.RecordUse(ctx, cred.ID, cred.Authenticator.SignCount, cred.Flags.ProtocolValue()); err != nil {
		// Non-fatal: the assertion verified. A stale counter only
		// weakens clone detection for the next login.
		slog.Warn("passkey: record use failed", "user_id", ident.User.ID, "error", err)
	}
	if s.pool != nil {
		_, _ = s.pool.Exec(ctx, `UPDATE platform_users SET last_sign_in_at = now() WHERE id = $1`, ident.User.ID)
	}
	token, expiresIn, err := s.generatePlatformJWT(ident.User.ID, ident.User.Email, ident.IsSuperadmin, LoginViaPasskey, "")
	if err != nil {
		return nil, err
	}
	slog.Info("platform user signed in with passkey", "user_id", ident.User.ID, "mode", mode)
	s.audit(ctx, ident.User.ID, ident.User.Email, audit.ActionPasskeySignIn, meta, map[string]interface{}{
		"mode": mode,
	})
	return &PlatformAuthResponse{
		AccessToken: token,
		TokenType:   "bearer",
		ExpiresIn:   expiresIn,
		User:        ident.User,
	}, nil
}

// resetPasswordClearingPasskeys sets the new password hash and deletes
// every passkey on the account in ONE transaction — the MVP
// lost-all-passkeys escape hatch (#621, "option 1": email compromise ⇒
// account compromise is an accepted trade-off until recovery codes
// land). Atomic so a failure can't leave MFA stripped with the old
// password still in place and no notice sent. Returns the account
// email and how many passkeys were removed.
func (s *PlatformAuthService) resetPasswordClearingPasskeys(ctx context.Context, userID, passwordHash string) (string, int, error) {
	return s.passkeys.ResetPasswordClearingPasskeys(ctx, userID, passwordHash)
}

func normalizeNickname(nickname string) (*string, error) {
	nickname = strings.TrimSpace(nickname)
	if nickname == "" {
		return nil, nil
	}
	if len([]rune(nickname)) > maxPasskeyNicknameLen {
		return nil, ErrPasskeyNicknameInvalid
	}
	return &nickname, nil
}

// describeWebAuthnErr surfaces go-webauthn's DevInfo (the useful part)
// for logs + audit metadata.
func describeWebAuthnErr(err error) string {
	var perr *protocol.Error
	if errors.As(err, &perr) {
		if perr.DevInfo != "" {
			return perr.Details + ": " + perr.DevInfo
		}
		return perr.Details
	}
	return err.Error()
}

// ── Postgres store ─────────────────────────────────────────────────

type pgPasskeyStore struct {
	pool *pgxpool.Pool
}

const passkeyColumns = `id::text, credential_id, public_key, attestation_type, attestation_format,
	transports, flags, aaguid, sign_count, nickname, created_at, last_used_at`

func scanPasskey(row pgx.Row) (storedPasskey, error) {
	var sp storedPasskey
	var transports []string
	var flags int16
	var signCount int64
	err := row.Scan(&sp.Info.ID, &sp.Credential.ID, &sp.Credential.PublicKey,
		&sp.Credential.AttestationType, &sp.Credential.AttestationFormat,
		&transports, &flags, &sp.Credential.Authenticator.AAGUID, &signCount,
		&sp.Info.Nickname, &sp.Info.CreatedAt, &sp.Info.LastUsedAt)
	if err != nil {
		return sp, err
	}
	sp.Credential.Flags = webauthn.NewCredentialFlags(protocol.AuthenticatorFlags(byte(flags)))
	sp.Credential.Authenticator.SignCount = uint32(signCount)
	for _, t := range transports {
		sp.Credential.Transport = append(sp.Credential.Transport, protocol.AuthenticatorTransport(t))
	}
	sp.Info.Transports = transports
	if sp.Info.Transports == nil {
		sp.Info.Transports = []string{}
	}
	sp.Info.BackedUp = sp.Credential.Flags.BackupState
	return sp, nil
}

func (p *pgPasskeyStore) ListCredentials(ctx context.Context, userID string) ([]storedPasskey, error) {
	rows, err := p.pool.Query(ctx,
		`SELECT `+passkeyColumns+`
		   FROM public.platform_passkey_credentials
		  WHERE platform_user_id = $1::uuid
		  ORDER BY created_at, id`,
		userID)
	if err != nil {
		return nil, fmt.Errorf("list passkeys: %w", err)
	}
	defer rows.Close()
	var out []storedPasskey
	for rows.Next() {
		sp, err := scanPasskey(rows)
		if err != nil {
			return nil, fmt.Errorf("scan passkey: %w", err)
		}
		out = append(out, sp)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate passkeys: %w", err)
	}
	return out, nil
}

func (p *pgPasskeyStore) CountCredentials(ctx context.Context, userID string) (int, error) {
	var n int
	err := p.pool.QueryRow(ctx,
		`SELECT count(*) FROM public.platform_passkey_credentials WHERE platform_user_id = $1::uuid`,
		userID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count passkeys: %w", err)
	}
	return n, nil
}

func (p *pgPasskeyStore) InsertCredential(ctx context.Context, userID string, cred *webauthn.Credential, nickname *string) (*PasskeyInfo, error) {
	transports := make([]string, 0, len(cred.Transport))
	for _, t := range cred.Transport {
		transports = append(transports, string(t))
	}
	var aaguid []byte
	if len(cred.Authenticator.AAGUID) > 0 {
		aaguid = cred.Authenticator.AAGUID
	}
	sp, err := scanPasskey(p.pool.QueryRow(ctx,
		`INSERT INTO public.platform_passkey_credentials
		   (platform_user_id, credential_id, public_key, attestation_type, attestation_format,
		    transports, flags, aaguid, sign_count, nickname)
		 VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 RETURNING `+passkeyColumns,
		userID, cred.ID, cred.PublicKey, cred.AttestationType, cred.AttestationFormat,
		transports, int16(cred.Flags.ProtocolValue()), aaguid, int64(cred.Authenticator.SignCount), nickname))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrPasskeyAlreadyRegistered
		}
		return nil, fmt.Errorf("insert passkey: %w", err)
	}
	return &sp.Info, nil
}

func (p *pgPasskeyStore) RecordUse(ctx context.Context, credentialID []byte, signCount uint32, flags protocol.AuthenticatorFlags) error {
	_, err := p.pool.Exec(ctx,
		`UPDATE public.platform_passkey_credentials
		    SET sign_count = $2, flags = $3, last_used_at = now()
		  WHERE credential_id = $1`,
		credentialID, int64(signCount), int16(flags))
	return err
}

func (p *pgPasskeyStore) RenameCredential(ctx context.Context, userID, id string, nickname *string) error {
	if _, err := uuid.Parse(id); err != nil {
		return ErrPasskeyNotFound
	}
	tag, err := p.pool.Exec(ctx,
		`UPDATE public.platform_passkey_credentials SET nickname = $3
		  WHERE id = $2::uuid AND platform_user_id = $1::uuid`,
		userID, id, nickname)
	if err != nil {
		return fmt.Errorf("rename passkey: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrPasskeyNotFound
	}
	return nil
}

func (p *pgPasskeyStore) DeleteCredential(ctx context.Context, userID, id string) error {
	if _, err := uuid.Parse(id); err != nil {
		return ErrPasskeyNotFound
	}
	tag, err := p.pool.Exec(ctx,
		`DELETE FROM public.platform_passkey_credentials
		  WHERE id = $2::uuid AND platform_user_id = $1::uuid`,
		userID, id)
	if err != nil {
		return fmt.Errorf("delete passkey: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrPasskeyNotFound
	}
	return nil
}

func (p *pgPasskeyStore) SaveChallenge(ctx context.Context, userID, purpose string, sd *webauthn.SessionData) (string, error) {
	data, err := json.Marshal(sd)
	if err != nil {
		return "", fmt.Errorf("marshal session data: %w", err)
	}
	// Opportunistic sweep of abandoned ceremonies. Bounded by the
	// expires_at index; cheap because rows only live for challengeTTL.
	if _, err := p.pool.Exec(ctx,
		`DELETE FROM public.platform_webauthn_challenges WHERE expires_at < now() - interval '1 minute'`,
	); err != nil {
		slog.Warn("passkey: challenge sweep failed", "error", err)
	}
	var uid interface{}
	if userID != "" {
		uid = userID
	}
	var id string
	err = p.pool.QueryRow(ctx,
		`INSERT INTO public.platform_webauthn_challenges (platform_user_id, purpose, session_data, expires_at)
		 VALUES ($1::uuid, $2, $3, now() + make_interval(secs => $4))
		 RETURNING id::text`,
		uid, purpose, data, challengeTTL.Seconds()).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("store challenge: %w", err)
	}
	return id, nil
}

func (p *pgPasskeyStore) MarkChallengeUsed(ctx context.Context, id string, expires time.Time) (bool, error) {
	if _, err := uuid.Parse(id); err != nil {
		return false, ErrPasskeyChallengeInvalid
	}
	// Row lives until the token itself would have expired; the sweep
	// in SaveChallenge removes it afterwards.
	tag, err := p.pool.Exec(ctx,
		`INSERT INTO public.platform_webauthn_challenges (id, purpose, session_data, expires_at)
		 VALUES ($1::uuid, 'login', '{}'::jsonb, $2)
		 ON CONFLICT (id) DO NOTHING`,
		id, expires)
	if err != nil {
		return false, fmt.Errorf("mark challenge used: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (p *pgPasskeyStore) ResetPasswordClearingPasskeys(ctx context.Context, userID, passwordHash string) (string, int, error) {
	// Developer pool: eurobase_developer reaches platform_users via its
	// INHERIT membership in the owning eurobase_migrator role.
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return "", 0, fmt.Errorf("begin reset tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // committed path returns first
	tag, err := tx.Exec(ctx,
		`DELETE FROM public.platform_passkey_credentials WHERE platform_user_id = $1::uuid`, userID)
	if err != nil {
		return "", 0, fmt.Errorf("clear passkeys: %w", err)
	}
	var email string
	if err := tx.QueryRow(ctx,
		`UPDATE public.platform_users SET password_hash = $1 WHERE id = $2::uuid RETURNING email`,
		passwordHash, userID).Scan(&email); err != nil {
		return "", 0, fmt.Errorf("update password: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", 0, fmt.Errorf("commit reset: %w", err)
	}
	return email, int(tag.RowsAffected()), nil
}

func (p *pgPasskeyStore) ConsumeChallenge(ctx context.Context, id, purpose string) (string, *webauthn.SessionData, error) {
	if _, err := uuid.Parse(id); err != nil {
		return "", nil, ErrPasskeyChallengeInvalid
	}
	var userID *string
	var data []byte
	err := p.pool.QueryRow(ctx,
		`DELETE FROM public.platform_webauthn_challenges
		  WHERE id = $1::uuid AND purpose = $2 AND expires_at > now()
		  RETURNING platform_user_id::text, session_data`,
		id, purpose).Scan(&userID, &data)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil, ErrPasskeyChallengeInvalid
		}
		return "", nil, fmt.Errorf("consume challenge: %w", err)
	}
	var sd webauthn.SessionData
	if err := json.Unmarshal(data, &sd); err != nil {
		return "", nil, fmt.Errorf("decode session data: %w", err)
	}
	uid := ""
	if userID != nil {
		uid = *userID
	}
	return uid, &sd, nil
}
