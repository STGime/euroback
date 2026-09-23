package auth

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/protocol/webauthncbor"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"

	"github.com/eurobase/euroback/internal/audit"
)

// Unit tests for the console passkey ceremonies (#621). A minimal
// software authenticator (ECDSA P-256, "none" attestation) drives real
// go-webauthn verification end to end against an in-memory store — no
// Postgres, no browser.

const (
	testRPID   = "console.eurobase.test"
	testOrigin = "https://console.eurobase.test"
)

var b64 = base64.RawURLEncoding

// ── software authenticator ─────────────────────────────────────────

type softAuthenticator struct {
	key        *ecdsa.PrivateKey
	credID     []byte
	userHandle []byte
	counter    uint32
	// Knobs for negative tests.
	origin   string
	skipUV   bool
	rpIDHash []byte
	// zeroCounter mimics synced passkeys (iCloud / Google), which
	// always report sign count 0 — so clone detection can't catch a
	// replay and single-use challenges must.
	zeroCounter bool
}

func newSoftAuthenticator(t *testing.T) *softAuthenticator {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	h := sha256.Sum256([]byte(testRPID))
	return &softAuthenticator{key: key, credID: id, origin: testOrigin, rpIDHash: h[:]}
}

func (a *softAuthenticator) flags(attested bool) byte {
	f := byte(protocol.FlagUserPresent)
	if !a.skipUV {
		f |= byte(protocol.FlagUserVerified)
	}
	if attested {
		f |= byte(protocol.FlagAttestedCredentialData)
	}
	return f
}

func challengeFrom(t *testing.T, options json.RawMessage) string {
	t.Helper()
	var o struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
			User      struct {
				ID string `json:"id"`
			} `json:"user"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(options, &o); err != nil {
		t.Fatalf("parse options: %v", err)
	}
	return o.PublicKey.Challenge
}

func (a *softAuthenticator) clientData(typ, challenge string) []byte {
	b, _ := json.Marshal(map[string]string{"type": typ, "challenge": challenge, "origin": a.origin})
	return b
}

// register builds the attestation response for navigator.credentials.create.
func (a *softAuthenticator) register(t *testing.T, ch *PasskeyChallenge) []byte {
	t.Helper()
	var o struct {
		PublicKey struct {
			User struct {
				ID string `json:"id"`
			} `json:"user"`
		} `json:"publicKey"`
	}
	_ = json.Unmarshal(ch.Options, &o)
	a.userHandle, _ = b64.DecodeString(o.PublicKey.User.ID)

	cose, err := webauthncbor.Marshal(map[int]interface{}{
		1:  2,  // kty: EC2
		3:  -7, // alg: ES256
		-1: 1,  // crv: P-256
		-2: a.key.PublicKey.X.FillBytes(make([]byte, 32)),
		-3: a.key.PublicKey.Y.FillBytes(make([]byte, 32)),
	})
	if err != nil {
		t.Fatal(err)
	}
	var authData bytes.Buffer
	authData.Write(a.rpIDHash)
	authData.WriteByte(a.flags(true))
	_ = binary.Write(&authData, binary.BigEndian, a.counter)
	authData.Write(make([]byte, 16)) // AAGUID
	_ = binary.Write(&authData, binary.BigEndian, uint16(len(a.credID)))
	authData.Write(a.credID)
	authData.Write(cose)

	attObj, err := webauthncbor.Marshal(map[string]interface{}{
		"fmt":      "none",
		"attStmt":  map[string]interface{}{},
		"authData": authData.Bytes(),
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, _ := json.Marshal(map[string]interface{}{
		"id":    b64.EncodeToString(a.credID),
		"rawId": b64.EncodeToString(a.credID),
		"type":  "public-key",
		"response": map[string]interface{}{
			"clientDataJSON":    b64.EncodeToString(a.clientData("webauthn.create", challengeFrom(t, ch.Options))),
			"attestationObject": b64.EncodeToString(attObj),
			"transports":        []string{"internal"},
		},
	})
	return resp
}

// assert builds the assertion response for navigator.credentials.get.
func (a *softAuthenticator) assert(t *testing.T, options json.RawMessage) []byte {
	t.Helper()
	if !a.zeroCounter {
		a.counter++
	}
	var authData bytes.Buffer
	authData.Write(a.rpIDHash)
	authData.WriteByte(a.flags(false))
	_ = binary.Write(&authData, binary.BigEndian, a.counter)

	cd := a.clientData("webauthn.get", challengeFrom(t, options))
	cdHash := sha256.Sum256(cd)
	digest := sha256.Sum256(append(authData.Bytes(), cdHash[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, a.key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	resp, _ := json.Marshal(map[string]interface{}{
		"id":    b64.EncodeToString(a.credID),
		"rawId": b64.EncodeToString(a.credID),
		"type":  "public-key",
		"response": map[string]interface{}{
			"clientDataJSON":    b64.EncodeToString(cd),
			"authenticatorData": b64.EncodeToString(authData.Bytes()),
			"signature":         b64.EncodeToString(sig),
			"userHandle":        b64.EncodeToString(a.userHandle),
		},
	})
	return resp
}

// ── in-memory store + fakes ────────────────────────────────────────

type memChallenge struct {
	userID  string
	purpose string
	sd      webauthn.SessionData
	expires time.Time
}

type memPasskeyStore struct {
	mu         sync.Mutex
	creds      map[string][]storedPasskey // userID → creds
	challenges map[string]memChallenge
	used       map[string]bool
	passwords  map[string]string
}

func newMemPasskeyStore() *memPasskeyStore {
	return &memPasskeyStore{creds: map[string][]storedPasskey{}, challenges: map[string]memChallenge{}, used: map[string]bool{}, passwords: map[string]string{}}
}

func (m *memPasskeyStore) ListCredentials(_ context.Context, userID string) ([]storedPasskey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]storedPasskey(nil), m.creds[userID]...), nil
}

func (m *memPasskeyStore) CountCredentials(_ context.Context, userID string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.creds[userID]), nil
}

func (m *memPasskeyStore) InsertCredential(_ context.Context, userID string, cred *webauthn.Credential, nickname *string) (*PasskeyInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, list := range m.creds {
		for _, sp := range list {
			if bytes.Equal(sp.Credential.ID, cred.ID) {
				return nil, ErrPasskeyAlreadyRegistered
			}
		}
	}
	sp := storedPasskey{
		Info:       PasskeyInfo{ID: uuid.NewString(), Nickname: nickname, CreatedAt: time.Now(), BackedUp: cred.Flags.BackupState},
		Credential: *cred,
	}
	m.creds[userID] = append(m.creds[userID], sp)
	return &sp.Info, nil
}

func (m *memPasskeyStore) RecordUse(_ context.Context, credentialID []byte, signCount uint32, _ protocol.AuthenticatorFlags) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for u, list := range m.creds {
		for i := range list {
			if bytes.Equal(list[i].Credential.ID, credentialID) {
				m.creds[u][i].Credential.Authenticator.SignCount = signCount
				now := time.Now()
				m.creds[u][i].Info.LastUsedAt = &now
			}
		}
	}
	return nil
}

func (m *memPasskeyStore) RenameCredential(_ context.Context, userID, id string, nickname *string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.creds[userID] {
		if m.creds[userID][i].Info.ID == id {
			m.creds[userID][i].Info.Nickname = nickname
			return nil
		}
	}
	return ErrPasskeyNotFound
}

func (m *memPasskeyStore) DeleteCredential(_ context.Context, userID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := m.creds[userID]
	for i := range list {
		if list[i].Info.ID == id {
			m.creds[userID] = append(list[:i], list[i+1:]...)
			return nil
		}
	}
	return ErrPasskeyNotFound
}

func (m *memPasskeyStore) ResetPasswordClearingPasskeys(_ context.Context, userID, passwordHash string) (string, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := len(m.creds[userID])
	delete(m.creds, userID)
	m.passwords[userID] = passwordHash
	return userID + "@example.eu", n, nil
}

func (m *memPasskeyStore) MarkChallengeUsed(_ context.Context, id string, _ time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.used[id] {
		return false, nil
	}
	m.used[id] = true
	return true, nil
}

func (m *memPasskeyStore) SaveChallenge(_ context.Context, userID, purpose string, sd *webauthn.SessionData) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := uuid.NewString()
	m.challenges[id] = memChallenge{userID: userID, purpose: purpose, sd: *sd, expires: time.Now().Add(challengeTTL)}
	return id, nil
}

func (m *memPasskeyStore) ConsumeChallenge(_ context.Context, id, purpose string) (string, *webauthn.SessionData, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.challenges[id]
	if !ok || c.purpose != purpose || time.Now().After(c.expires) {
		return "", nil, ErrPasskeyChallengeInvalid
	}
	delete(m.challenges, id)
	return c.userID, &c.sd, nil
}

type capturedAudit struct {
	actorID string
	action  string
}

type fakeAuditor struct {
	mu      sync.Mutex
	entries []capturedAudit
}

func (f *fakeAuditor) Log(_ context.Context, _, actorID, _, action string, _ ...audit.LogOption) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = append(f.entries, capturedAudit{actorID: actorID, action: action})
}

func (f *fakeAuditor) has(actorID, action string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range f.entries {
		if e.actorID == actorID && e.action == action {
			return true
		}
	}
	return false
}

type passkeyFixture struct {
	svc     *PlatformAuthService
	store   *memPasskeyStore
	auditor *fakeAuditor
	users   map[string]*passkeyIdentity
}

func newPasskeyFixture(t *testing.T) *passkeyFixture {
	t.Helper()
	f := &passkeyFixture{
		svc:     NewPlatformAuthService(nil, "test-secret-test-secret-test-secret"),
		store:   newMemPasskeyStore(),
		auditor: &fakeAuditor{},
		users:   map[string]*passkeyIdentity{},
	}
	f.svc.lookupIdentity = func(_ context.Context, userID string) (*passkeyIdentity, error) {
		if u, ok := f.users[userID]; ok {
			return u, nil
		}
		return nil, errors.New("user not found")
	}
	f.svc.checkPassword = func(_ context.Context, _ string, password string) (bool, error) {
		return password == "correct horse battery staple", nil
	}
	cfg := PasskeyConfig{RPID: testRPID, RPDisplayName: "Test", RPOrigins: []string{testOrigin}}
	if err := f.svc.enablePasskeysWithStore(cfg, f.store, f.auditor); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *passkeyFixture) addUser(email string) string {
	id := uuid.NewString()
	f.users[id] = &passkeyIdentity{User: PlatformUser{ID: id, Email: email}, EmailConfirmed: true}
	return id
}

func (f *passkeyFixture) enrol(t *testing.T, userID string) *softAuthenticator {
	t.Helper()
	ctx := context.Background()
	a := newSoftAuthenticator(t)
	ch, err := f.svc.BeginPasskeyRegistration(ctx, userID)
	if err != nil {
		t.Fatalf("begin registration: %v", err)
	}
	if _, err := f.svc.FinishPasskeyRegistration(ctx, userID, ch.ChallengeID, a.register(t, ch), "laptop", PasskeyRequestMeta{}); err != nil {
		t.Fatalf("finish registration: %v", err)
	}
	return a
}

func (f *passkeyFixture) loginVia(t *testing.T, token string) string {
	t.Helper()
	claims, err := f.svc.ValidatePlatformJWT(token)
	if err != nil {
		t.Fatalf("validate jwt: %v", err)
	}
	return claims.LoginVia
}

// ── tests ──────────────────────────────────────────────────────────

func TestPasskey_RegisterThenUsernamelessLogin(t *testing.T) {
	f := newPasskeyFixture(t)
	ctx := context.Background()
	uid := f.addUser("alice@example.eu")
	a := f.enrol(t, uid)

	list, err := f.svc.ListPasskeys(ctx, uid)
	if err != nil || len(list) != 1 || list[0].Nickname == nil || *list[0].Nickname != "laptop" {
		t.Fatalf("list after enrol = %+v, %v", list, err)
	}
	if !f.auditor.has(uid, audit.ActionPasskeyRegistered) {
		t.Error("registration not audited")
	}

	ch, err := f.svc.BeginPasskeyLogin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := f.svc.FinishPasskeyLogin(ctx, ch.ChallengeID, a.assert(t, ch.Options), PasskeyRequestMeta{})
	if err != nil {
		t.Fatalf("passkey login: %v", err)
	}
	if resp.User.ID != uid || resp.AccessToken == "" {
		t.Fatalf("unexpected response %+v", resp)
	}
	if got := f.loginVia(t, resp.AccessToken); got != LoginViaPasskey {
		t.Fatalf("login_via = %q, want %q", got, LoginViaPasskey)
	}
	if !f.auditor.has(uid, audit.ActionPasskeySignIn) {
		t.Error("sign-in not audited")
	}
}

func TestPasskey_StepUpAfterPassword(t *testing.T) {
	f := newPasskeyFixture(t)
	ctx := context.Background()
	uid := f.addUser("bob@example.eu")
	a := f.enrol(t, uid)

	ch, err := f.svc.beginStepUp(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := f.svc.FinishStepUp(ctx, ch.ChallengeID, a.assert(t, ch.Options), PasskeyRequestMeta{})
	if err != nil {
		t.Fatalf("step-up: %v", err)
	}
	if got := f.loginVia(t, resp.AccessToken); got != LoginViaPasskey {
		t.Fatalf("login_via = %q, want passkey", got)
	}
}

func TestPasskey_ChallengeIsSingleUse(t *testing.T) {
	f := newPasskeyFixture(t)
	ctx := context.Background()
	uid := f.addUser("carol@example.eu")
	a := f.enrol(t, uid)
	a.zeroCounter = true

	ch, _ := f.svc.BeginPasskeyLogin(ctx)
	assertion := a.assert(t, ch.Options)
	if _, err := f.svc.FinishPasskeyLogin(ctx, ch.ChallengeID, assertion, PasskeyRequestMeta{}); err != nil {
		t.Fatalf("first use: %v", err)
	}
	if _, err := f.svc.FinishPasskeyLogin(ctx, ch.ChallengeID, assertion, PasskeyRequestMeta{}); !errors.Is(err, ErrPasskeyChallengeInvalid) {
		t.Fatalf("replay: got %v, want ErrPasskeyChallengeInvalid", err)
	}
}

func TestPasskey_ChallengePurposeIsEnforced(t *testing.T) {
	f := newPasskeyFixture(t)
	ctx := context.Background()
	uid := f.addUser("dan@example.eu")
	a := f.enrol(t, uid)

	// A username-less login challenge must not satisfy a step-up (the
	// step-up token is the "password already verified" proof).
	ch, _ := f.svc.BeginPasskeyLogin(ctx)
	if _, err := f.svc.FinishStepUp(ctx, ch.ChallengeID, a.assert(t, ch.Options), PasskeyRequestMeta{}); !errors.Is(err, ErrPasskeyChallengeInvalid) {
		t.Fatalf("login challenge as step-up: got %v", err)
	}
}

func TestPasskey_StepUpRejectsOtherUsersPasskey(t *testing.T) {
	f := newPasskeyFixture(t)
	ctx := context.Background()
	victim := f.addUser("victim@example.eu")
	attacker := f.addUser("attacker@example.eu")
	f.enrol(t, victim)
	attackerKey := f.enrol(t, attacker)

	// Attacker knows the victim's password (step-up issued for victim)
	// but only holds their own passkey.
	ch, err := f.svc.beginStepUp(ctx, victim)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.FinishStepUp(ctx, ch.ChallengeID, attackerKey.assert(t, ch.Options), PasskeyRequestMeta{}); !errors.Is(err, ErrPasskeyVerificationFailed) {
		t.Fatalf("foreign passkey on step-up: got %v, want verification failure", err)
	}
	if !f.auditor.has(victim, audit.ActionPasskeySignInFailed) {
		t.Error("failed step-up not audited against the victim account")
	}
}

func TestPasskey_RejectsWrongOrigin(t *testing.T) {
	f := newPasskeyFixture(t)
	ctx := context.Background()
	uid := f.addUser("erin@example.eu")
	a := f.enrol(t, uid)

	// e.g. a tenant app on another eurobase subdomain
	a.origin = "https://evil.eurobase.test"
	ch, _ := f.svc.BeginPasskeyLogin(ctx)
	if _, err := f.svc.FinishPasskeyLogin(ctx, ch.ChallengeID, a.assert(t, ch.Options), PasskeyRequestMeta{}); !errors.Is(err, ErrPasskeyVerificationFailed) {
		t.Fatalf("wrong origin: got %v", err)
	}
}

func TestPasskey_RequiresUserVerification(t *testing.T) {
	f := newPasskeyFixture(t)
	ctx := context.Background()
	uid := f.addUser("frank@example.eu")

	// Registration without UV is refused…
	a := newSoftAuthenticator(t)
	a.skipUV = true
	ch, _ := f.svc.BeginPasskeyRegistration(ctx, uid)
	if _, err := f.svc.FinishPasskeyRegistration(ctx, uid, ch.ChallengeID, a.register(t, ch), "", PasskeyRequestMeta{}); !errors.Is(err, ErrPasskeyVerificationFailed) {
		t.Fatalf("register without UV: got %v", err)
	}

	// …and so is an assertion without UV (presence-only isn't MFA).
	b := f.enrol(t, uid)
	b.skipUV = true
	lch, _ := f.svc.BeginPasskeyLogin(ctx)
	if _, err := f.svc.FinishPasskeyLogin(ctx, lch.ChallengeID, b.assert(t, lch.Options), PasskeyRequestMeta{}); !errors.Is(err, ErrPasskeyVerificationFailed) {
		t.Fatalf("login without UV: got %v", err)
	}
}

func TestPasskey_CounterRegressionIsRefused(t *testing.T) {
	f := newPasskeyFixture(t)
	ctx := context.Background()
	uid := f.addUser("gina@example.eu")
	a := f.enrol(t, uid)

	a.counter = 10
	ch, _ := f.svc.BeginPasskeyLogin(ctx)
	if _, err := f.svc.FinishPasskeyLogin(ctx, ch.ChallengeID, a.assert(t, ch.Options), PasskeyRequestMeta{}); err != nil {
		t.Fatalf("login: %v", err)
	}
	// A clone replaying an older counter value.
	a.counter = 3
	ch, _ = f.svc.BeginPasskeyLogin(ctx)
	if _, err := f.svc.FinishPasskeyLogin(ctx, ch.ChallengeID, a.assert(t, ch.Options), PasskeyRequestMeta{}); !errors.Is(err, ErrPasskeyVerificationFailed) {
		t.Fatalf("counter regression: got %v", err)
	}
}

func TestPasskey_RegisterChallengeBoundToUser(t *testing.T) {
	f := newPasskeyFixture(t)
	ctx := context.Background()
	alice := f.addUser("alice2@example.eu")
	mallory := f.addUser("mallory@example.eu")

	ch, _ := f.svc.BeginPasskeyRegistration(ctx, alice)
	a := newSoftAuthenticator(t)
	if _, err := f.svc.FinishPasskeyRegistration(ctx, mallory, ch.ChallengeID, a.register(t, ch), "", PasskeyRequestMeta{}); !errors.Is(err, ErrPasskeyChallengeInvalid) {
		t.Fatalf("cross-user register finish: got %v", err)
	}
}

func TestPasskey_DuplicateEnrolmentAndDelete(t *testing.T) {
	f := newPasskeyFixture(t)
	ctx := context.Background()
	uid := f.addUser("hank@example.eu")
	f.enrol(t, uid)

	list, _ := f.svc.ListPasskeys(ctx, uid)
	if err := f.svc.RenamePasskey(ctx, uid, list[0].ID, "work laptop"); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.DeletePasskey(ctx, uid, "hank@example.eu", list[0].ID, PasskeyRequestMeta{}); err != nil {
		t.Fatal(err)
	}
	if n, _ := f.store.CountCredentials(ctx, uid); n != 0 {
		t.Fatalf("count after delete = %d", n)
	}
	if !f.auditor.has(uid, audit.ActionPasskeyRemoved) {
		t.Error("removal not audited")
	}
	if err := f.svc.DeletePasskey(ctx, uid, "hank@example.eu", list[0].ID, PasskeyRequestMeta{}); !errors.Is(err, ErrPasskeyNotFound) {
		t.Fatalf("second delete: got %v", err)
	}
}

func TestPasskey_ClearForReset(t *testing.T) {
	f := newPasskeyFixture(t)
	ctx := context.Background()
	uid := f.addUser("ivy@example.eu")
	f.enrol(t, uid)
	f.enrol(t, uid)

	_, n, err := f.svc.resetPasswordClearingPasskeys(ctx, uid, "new-hash")
	if err != nil || n != 2 {
		t.Fatalf("clear = %d, %v; want 2", n, err)
	}
	if f.store.passwords[uid] != "new-hash" {
		t.Fatal("password not updated in the same operation")
	}
	if c, _ := f.store.CountCredentials(ctx, uid); c != 0 {
		t.Fatalf("passkeys left after reset: %d", c)
	}
}

func TestPasskey_ReauthRule(t *testing.T) {
	f := newPasskeyFixture(t)
	ctx := context.Background()

	fresh := &Claims{Subject: "u", IssuedAt: time.Now().Add(-time.Minute)}
	if err := f.svc.CheckPasskeyReauth(ctx, fresh, ""); err != nil {
		t.Fatalf("fresh session: %v", err)
	}
	stale := &Claims{Subject: "u", IssuedAt: time.Now().Add(-2 * time.Hour)}
	if err := f.svc.CheckPasskeyReauth(ctx, stale, ""); !errors.Is(err, ErrReauthRequired) {
		t.Fatalf("stale, no password: %v", err)
	}
	if err := f.svc.CheckPasskeyReauth(ctx, stale, "wrong"); !errors.Is(err, ErrReauthRequired) {
		t.Fatalf("stale, wrong password: %v", err)
	}
	if err := f.svc.CheckPasskeyReauth(ctx, stale, "correct horse battery staple"); err != nil {
		t.Fatalf("stale, right password: %v", err)
	}
	// PAT-style claims carry no iat → always need the password.
	if err := f.svc.CheckPasskeyReauth(ctx, &Claims{Subject: "u"}, ""); !errors.Is(err, ErrReauthRequired) {
		t.Fatalf("no iat: %v", err)
	}
}

func TestPasskey_DisabledReturnsUnavailable(t *testing.T) {
	svc := NewPlatformAuthService(nil, "secret")
	if _, err := svc.BeginPasskeyLogin(context.Background()); !errors.Is(err, ErrPasskeysUnavailable) {
		t.Fatalf("got %v", err)
	}
}

func TestPasskeyConfigFromConsoleURL(t *testing.T) {
	cfg, err := PasskeyConfigFromConsoleURL("https://console.eurobase.app/", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RPID != "console.eurobase.app" || len(cfg.RPOrigins) != 1 || cfg.RPOrigins[0] != "https://console.eurobase.app" {
		t.Fatalf("derived %+v", cfg)
	}
	cfg, err = PasskeyConfigFromConsoleURL("http://localhost:5173", "", "")
	if err != nil || cfg.RPID != "localhost" || cfg.RPOrigins[0] != "http://localhost:5173" {
		t.Fatalf("localhost: %+v %v", cfg, err)
	}
	cfg, err = PasskeyConfigFromConsoleURL("https://ignored.example", "staging.eurobase.app", "https://a.example, https://b.example/")
	if err != nil || cfg.RPID != "staging.eurobase.app" || len(cfg.RPOrigins) != 2 || cfg.RPOrigins[1] != "https://b.example" {
		t.Fatalf("overrides: %+v %v", cfg, err)
	}
	if _, err := PasskeyConfigFromConsoleURL("", "", ""); err == nil {
		t.Fatal("empty config should error")
	}
}

func TestPasskey_LoginChallengeTokenTamperAndExpiry(t *testing.T) {
	f := newPasskeyFixture(t)
	ctx := context.Background()
	uid := f.addUser("jo@example.eu")
	a := f.enrol(t, uid)

	ch, _ := f.svc.BeginPasskeyLogin(ctx)
	body, sig, _ := strings.Cut(ch.ChallengeID, ".")
	// Flip a byte of the signed payload.
	tampered := body[:len(body)-2] + "AA" + "." + sig
	if _, err := f.svc.FinishPasskeyLogin(ctx, tampered, a.assert(t, ch.Options), PasskeyRequestMeta{}); !errors.Is(err, ErrPasskeyChallengeInvalid) {
		t.Fatalf("tampered token: got %v", err)
	}
	// A token signed with a different secret (another environment).
	other := newPasskeyFixture(t)
	other.svc.jwtSecret = []byte("some-other-secret-some-other-secret")
	och, _ := other.svc.BeginPasskeyLogin(ctx)
	if _, err := f.svc.FinishPasskeyLogin(ctx, och.ChallengeID, a.assert(t, och.Options), PasskeyRequestMeta{}); !errors.Is(err, ErrPasskeyChallengeInvalid) {
		t.Fatalf("foreign-key token: got %v", err)
	}
	// Expired token.
	sd := webauthn.SessionData{Challenge: "x"}
	payload, _ := json.Marshal(loginChallenge{ID: uuid.NewString(), Session: sd, Exp: time.Now().Add(-time.Minute).Unix()})
	b := base64.RawURLEncoding.EncodeToString(payload)
	m := hmac.New(sha256.New, f.svc.loginChallengeKey())
	m.Write([]byte(b))
	expired := b + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
	if _, err := f.svc.verifyLoginChallenge(expired); !errors.Is(err, ErrPasskeyChallengeInvalid) {
		t.Fatalf("expired token: got %v", err)
	}
}

func TestPasskey_SSOSessionNeedsPasswordForReauth(t *testing.T) {
	f := newPasskeyFixture(t)
	fresh := &Claims{Subject: "u", LoginVia: LoginViaSSO, IssuedAt: time.Now()}
	if err := f.svc.CheckPasskeyReauth(context.Background(), fresh, ""); !errors.Is(err, ErrReauthRequired) {
		t.Fatalf("fresh SSO session without password: got %v", err)
	}
	if err := f.svc.CheckPasskeyReauth(context.Background(), fresh, "correct horse battery staple"); err != nil {
		t.Fatalf("SSO session with password: %v", err)
	}
}

func TestPasskey_FailedLoginNotAuditedAgainstForeignHandle(t *testing.T) {
	f := newPasskeyFixture(t)
	ctx := context.Background()
	victim := f.addUser("victim2@example.eu")
	f.enrol(t, victim)
	attacker := f.addUser("attacker2@example.eu")
	a := f.enrol(t, attacker)

	// Attacker presents their own credential but claims the victim's
	// user handle.
	a.userHandle, _ = userHandle(victim)
	ch, _ := f.svc.BeginPasskeyLogin(ctx)
	if _, err := f.svc.FinishPasskeyLogin(ctx, ch.ChallengeID, a.assert(t, ch.Options), PasskeyRequestMeta{}); !errors.Is(err, ErrPasskeyVerificationFailed) {
		t.Fatalf("got %v", err)
	}
	if f.auditor.has(victim, audit.ActionPasskeySignInFailed) {
		t.Fatal("failure audited against an account whose credential wasn't presented")
	}
}
