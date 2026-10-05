package enduser

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/tenant"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestBuildWebAuthn(t *testing.T) {
	wa, err := buildWebAuthn(tenant.PasskeyConfig{Enabled: true, RPID: "app.example.com", Origins: []string{"https://app.example.com"}})
	if err != nil || wa == nil {
		t.Fatalf("valid config: wa=%v err=%v", wa, err)
	}
	for _, bad := range []tenant.PasskeyConfig{
		{Enabled: true, RPID: "", Origins: []string{"https://x"}},
		{Enabled: true, RPID: "x", Origins: nil},
	} {
		if _, err := buildWebAuthn(bad); !errors.Is(err, auth.ErrPasskeysUnavailable) {
			t.Errorf("buildWebAuthn(%+v) err = %v, want ErrPasskeysUnavailable", bad, err)
		}
	}
}

func TestLoginChallengeRoundTrip(t *testing.T) {
	secret := []byte("project-jwt-secret-abc")
	sd := &webauthn.SessionData{Challenge: "Y2hhbGxlbmdl"}

	token, err := signLoginChallenge(secret, sd)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	lc, err := verifyLoginChallenge(secret, token)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if lc.Session.Challenge != sd.Challenge || lc.ID == "" {
		t.Fatalf("round-trip mismatch: %+v", lc)
	}

	// Wrong project secret → reject (per-project isolation).
	if _, err := verifyLoginChallenge([]byte("other-secret"), token); !errors.Is(err, auth.ErrPasskeyChallengeInvalid) {
		t.Errorf("wrong secret: err = %v, want invalid", err)
	}
	// Tampered body → reject.
	if _, err := verifyLoginChallenge(secret, "x"+token); !errors.Is(err, auth.ErrPasskeyChallengeInvalid) {
		t.Errorf("tampered body accepted")
	}
	// No separator → reject.
	if _, err := verifyLoginChallenge(secret, "nodot"); !errors.Is(err, auth.ErrPasskeyChallengeInvalid) {
		t.Errorf("missing separator accepted")
	}
}

func TestLoginChallengeExpired(t *testing.T) {
	secret := []byte("s")
	// Craft a correctly-signed token whose exp is in the past — verify must
	// reject it on the expiry check, not the HMAC check.
	payload, _ := json.Marshal(loginChallenge{ID: uuid.NewString(), Exp: time.Now().Add(-time.Minute).Unix()})
	body := base64.RawURLEncoding.EncodeToString(payload)
	m := hmac.New(sha256.New, loginChallengeKey(secret))
	m.Write([]byte(body))
	token := body + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
	if _, err := verifyLoginChallenge(secret, token); !errors.Is(err, auth.ErrPasskeyChallengeInvalid) {
		t.Fatalf("expired token should be rejected, got %v", err)
	}
}

func TestNormalizePasskeyNickname(t *testing.T) {
	if n, err := normalizePasskeyNickname("  "); err != nil || n != nil {
		t.Errorf("blank → nil,nil; got %v,%v", n, err)
	}
	if n, err := normalizePasskeyNickname("  Phone  "); err != nil || n == nil || *n != "Phone" {
		t.Errorf("trim; got %v,%v", n, err)
	}
	long := make([]byte, maxPasskeyNicknameLen+1)
	for i := range long {
		long[i] = 'a'
	}
	if _, err := normalizePasskeyNickname(string(long)); !errors.Is(err, auth.ErrPasskeyNicknameInvalid) {
		t.Errorf("too long should error")
	}
}

// DB-backed smoke of the tenant-schema store SQL (column names, challenge
// consume + single-use). Needs a migrated PG16: ENDUSER_PASSKEY_TEST_URL.
func TestPasskeyStore_DB(t *testing.T) {
	url := os.Getenv("ENDUSER_PASSKEY_TEST_URL")
	if url == "" {
		t.Skip("ENDUSER_PASSKEY_TEST_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Skipf("connect: %v", err)
	}
	defer pool.Close()

	// Provision a tenant + one user.
	var schema, userID string
	pid := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO platform_users (email) VALUES ($1)`, "pk-store-"+pid+"@t.local"); err != nil {
		t.Fatal(err)
	}
	var ownerID string
	_ = pool.QueryRow(ctx, `SELECT id FROM platform_users WHERE email=$1`, "pk-store-"+pid+"@t.local").Scan(&ownerID)
	if _, err := pool.Exec(ctx, `INSERT INTO projects (id, owner_id, name, slug, schema_name, s3_bucket, region, plan, status)
		VALUES ($1,$2,'PKStore',$3,'tenant_pkstore','b','fr-par','free','provisioning')`, pid, ownerID, "pkstore-"+pid[:8]); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `SELECT provision_tenant($1,'PKStore','free')`, pid); err != nil {
		t.Fatal(err)
	}
	_ = pool.QueryRow(ctx, `SELECT schema_name FROM projects WHERE id=$1`, pid).Scan(&schema)
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `SELECT deprovision_tenant($1)`, pid)
		_, _ = pool.Exec(c, `DELETE FROM projects WHERE id=$1`, pid)
		_, _ = pool.Exec(c, `DELETE FROM platform_users WHERE id=$1`, ownerID)
	})
	if _, err := pool.Exec(ctx, `INSERT INTO `+quoteIdent(schema)+`.users (email) VALUES ($1) RETURNING id`, "u@t.local"); err != nil {
		t.Fatal(err)
	}
	_ = pool.QueryRow(ctx, `SELECT id::text FROM `+quoteIdent(schema)+`.users WHERE email=$1`, "u@t.local").Scan(&userID)

	svc := &AuthService{pool: pool}
	cred := &webauthn.Credential{ID: []byte("cred-rawid-1"), PublicKey: []byte("cose-key")}
	cred.Transport = append(cred.Transport, "internal")

	// insert → list → recordUse
	if err := svc.asService(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := insertCred(ctx, tx, schema, userID, cred, nil); e != nil {
			return e
		}
		got, e := listCreds(ctx, tx, schema, userID)
		if e != nil {
			return e
		}
		if len(got) != 1 || string(got[0].cred.ID) != "cred-rawid-1" {
			t.Fatalf("list = %+v", got)
		}
		return recordUse(ctx, tx, schema, cred.ID, 5)
	}); err != nil {
		t.Fatalf("insert/list/recordUse: %v", err)
	}

	// register challenge: save → consume (one-shot)
	if err := svc.asService(ctx, func(ctx context.Context, tx pgx.Tx) error {
		id, e := saveRegisterChallenge(ctx, tx, schema, userID, &webauthn.SessionData{Challenge: "abc"})
		if e != nil {
			return e
		}
		cu, sd, e := consumeRegisterChallenge(ctx, tx, schema, id)
		if e != nil || cu != userID || sd.Challenge != "abc" {
			t.Fatalf("consume: cu=%s sd=%v e=%v", cu, sd, e)
		}
		// second consume → invalid
		if _, _, e := consumeRegisterChallenge(ctx, tx, schema, id); !errors.Is(e, auth.ErrPasskeyChallengeInvalid) {
			t.Fatalf("second consume should be invalid, got %v", e)
		}
		return nil
	}); err != nil {
		t.Fatalf("challenge: %v", err)
	}

	// login challenge single-use
	lcID := uuid.NewString()
	if err := svc.asService(ctx, func(ctx context.Context, tx pgx.Tx) error {
		ok1, e := markLoginChallengeUsed(ctx, tx, schema, lcID, time.Now().Add(time.Minute))
		if e != nil || !ok1 {
			t.Fatalf("first mark: ok=%v e=%v", ok1, e)
		}
		ok2, e := markLoginChallengeUsed(ctx, tx, schema, lcID, time.Now().Add(time.Minute))
		if e != nil || ok2 {
			t.Fatalf("replay should be refused: ok=%v e=%v", ok2, e)
		}
		return nil
	}); err != nil {
		t.Fatalf("login single-use: %v", err)
	}
}
