package enduser

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/tenant"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// #808: a signed-in user changes their password with the current one; all
// refresh tokens are revoked and a fresh session comes back. Needs a
// migrated PG16, like TestPasskeyStore_DB: ENDUSER_PASSKEY_TEST_URL.
func TestChangePassword_DB(t *testing.T) {
	url := os.Getenv("ENDUSER_PASSKEY_TEST_URL")
	if url == "" {
		t.Skip("ENDUSER_PASSKEY_TEST_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Skipf("connect: %v", err)
	}
	t.Cleanup(pool.Close) // first, so it runs after the cleanup below

	pid := uuid.NewString()
	ownerEmail := "chpw-" + pid + "@t.local"
	if _, err := pool.Exec(ctx, `INSERT INTO platform_users (email) VALUES ($1)`, ownerEmail); err != nil {
		t.Fatal(err)
	}
	var ownerID, schema string
	_ = pool.QueryRow(ctx, `SELECT id FROM platform_users WHERE email=$1`, ownerEmail).Scan(&ownerID)
	if _, err := pool.Exec(ctx, `INSERT INTO projects (id, owner_id, name, slug, schema_name, s3_bucket, region, plan, status)
		VALUES ($1,$2,'ChPw',$3,'tenant_chpw',$4,'fr-par','free','provisioning')`, pid, ownerID, "chpw-"+pid[:8], "chpw-"+pid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `SELECT provision_tenant($1,'ChPw','free')`, pid); err != nil {
		t.Fatal(err)
	}
	_ = pool.QueryRow(ctx, `SELECT schema_name FROM projects WHERE id=$1`, pid).Scan(&schema)
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `SELECT deprovision_tenant($1)`, pid)
		_, _ = pool.Exec(c, `DELETE FROM projects WHERE id=$1`, pid)
		_, _ = pool.Exec(c, `DELETE FROM platform_users WHERE id=$1`, ownerID)
	})

	const jwtSecret = "test-jwt-secret-0123456789abcdef0123456789"
	raw := []byte(`{"providers": {"email_password": {"enabled": true}}, "password_min_length": 10, "session_duration": "1h"}`)
	cfg := tenant.ParseAuthConfig(raw)
	svc := &AuthService{pool: pool}
	var notified []string
	svc.passwordChangedHook = func(projectID, userEmail string) { notified = append(notified, projectID+" "+userEmail) }

	const email, oldPw, newPw = "member@example.com", "old-password-1", "new-password-2"
	up, err := svc.SignUp(ctx, schema, jwtSecret, pid, cfg, SignUpRequest{Email: email, Password: oldPw})
	if err != nil {
		t.Fatal(err)
	}
	otherDevice, err := svc.createRefreshToken(ctx, schema, up.User.ID)
	if err != nil {
		t.Fatal(err)
	}

	pc := &auth.ProjectContext{ProjectID: pid, SchemaName: schema, JWTSecret: jwtSecret, AuthConfig: raw}
	change := func(userID, current, next string) (int, map[string]any) {
		b, _ := json.Marshal(ChangePasswordRequest{CurrentPassword: current, NewPassword: next})
		r := httptest.NewRequest(http.MethodPost, "/v1/auth/user/password", bytes.NewReader(b))
		c := auth.ContextWithProject(r.Context(), pc)
		c = auth.ContextWithEndUserClaims(c, &auth.EndUserClaims{UserID: userID, Email: email, ProjectID: pid})
		w := httptest.NewRecorder()
		HandleChangePassword(svc)(w, r.WithContext(c))
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}

	if code, out := change(up.User.ID, "wrong-password", newPw); code != http.StatusBadRequest || out["error"] != ErrWrongCurrentPassword.Error() {
		t.Fatalf("wrong current password: %d %v", code, out)
	}
	if code, out := change(up.User.ID, oldPw, "short"); code != http.StatusBadRequest || out["error"] != "password must be at least 10 characters" {
		t.Fatalf("too short (project minimum 10): %d %v", code, out)
	}
	if code, _ := change(up.User.ID, oldPw, string(bytes.Repeat([]byte("x"), 73))); code != http.StatusBadRequest {
		t.Fatalf("over 72 bytes: %d", code)
	}

	if len(notified) != 0 {
		t.Fatalf("no notice for failed changes: %v", notified)
	}
	code, out := change(up.User.ID, oldPw, newPw)
	if code != http.StatusOK || out["access_token"] == nil || out["refresh_token"] == nil {
		t.Fatalf("change: %d %v", code, out)
	}
	if len(notified) != 1 || notified[0] != pid+" "+email {
		t.Fatalf("password changed notice (#822): %v", notified)
	}
	newRefresh, _ := out["refresh_token"].(string)

	// Old refresh tokens (this device's and another's) are revoked; the new one works.
	if _, err := svc.RefreshToken(ctx, schema, jwtSecret, pid, cfg, up.RefreshToken); err == nil {
		t.Fatal("this device's old refresh token must be revoked")
	}
	if _, err := svc.RefreshToken(ctx, schema, jwtSecret, pid, cfg, otherDevice); err == nil {
		t.Fatal("another device's refresh token must be revoked")
	}
	if _, err := svc.RefreshToken(ctx, schema, jwtSecret, pid, cfg, newRefresh); err != nil {
		t.Fatalf("the new refresh token: %v", err)
	}

	// Sign-in: the new password works, the old one doesn't.
	if _, err := svc.SignIn(ctx, schema, jwtSecret, pid, cfg, SignInRequest{Email: email, Password: newPw}); err != nil {
		t.Fatalf("sign in with the new password: %v", err)
	}
	if _, err := svc.SignIn(ctx, schema, jwtSecret, pid, cfg, SignInRequest{Email: email, Password: oldPw}); err == nil {
		t.Fatal("the old password must no longer work")
	}

	// No notice to an unconfirmed address (the setting off, so the change itself works).
	if _, err := pool.Exec(ctx, `UPDATE `+quoteIdent(schema)+`.users SET email_confirmed_at = NULL WHERE id = $1`, up.User.ID); err != nil {
		t.Fatal(err)
	}
	before := len(notified)
	if code, out := change(up.User.ID, newPw, "interim-password-9"); code != http.StatusOK {
		t.Fatalf("change (unconfirmed, setting off): %d %v", code, out)
	}
	if len(notified) != before {
		t.Fatalf("no notice to an unconfirmed address: %v", notified)
	}
	if code, out := change(up.User.ID, "interim-password-9", newPw); code != http.StatusOK {
		t.Fatalf("change back: %d %v", code, out)
	}

	// "Require email confirmation" on and the user unconfirmed: 403.
	if _, err := pool.Exec(ctx, `UPDATE `+quoteIdent(schema)+`.users SET email_confirmed_at = NULL WHERE id = $1`, up.User.ID); err != nil {
		t.Fatal(err)
	}
	confirmRaw := []byte(`{"providers": {"email_password": {"enabled": true}}, "password_min_length": 10, "session_duration": "1h", "require_email_confirmation": true, "redirect_urls": ["https://app.example.com/v"], "email_verification_url": "https://app.example.com/v"}`)
	pc.AuthConfig = confirmRaw
	if code, out := change(up.User.ID, newPw, "another-password-3"); code != http.StatusForbidden || out["error"] != ErrEmailNotConfirmed.Error() {
		t.Fatalf("unconfirmed: %d %v", code, out)
	}
	pc.AuthConfig = raw
	if _, err := pool.Exec(ctx, `UPDATE `+quoteIdent(schema)+`.users SET email_confirmed_at = now() WHERE id = $1`, up.User.ID); err != nil {
		t.Fatal(err)
	}

	// A user without a password, and a suspended user.
	var noPwID string
	if err := pool.QueryRow(ctx, `INSERT INTO `+quoteIdent(schema)+`.users (email, email_confirmed_at) VALUES ('magic@example.com', now()) RETURNING id::text`).Scan(&noPwID); err != nil {
		t.Fatal(err)
	}
	if code, out := change(noPwID, "anything-at-all", newPw); code != http.StatusBadRequest || out["error"] != ErrNoPassword.Error() {
		t.Fatalf("no password: %d %v", code, out)
	}
	if _, err := pool.Exec(ctx, `UPDATE `+quoteIdent(schema)+`.users SET banned_at = now() WHERE id = $1`, up.User.ID); err != nil {
		t.Fatal(err)
	}
	if code, _ := change(up.User.ID, newPw, "another-password-3"); code != http.StatusForbidden {
		t.Fatalf("suspended: %d", code)
	}
}
