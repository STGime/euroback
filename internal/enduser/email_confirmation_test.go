package enduser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/eurobase/euroback/internal/tenant"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// #806: with "Require email confirmation" on, an unconfirmed password user
// gets no session — not at sign-up, not by sign-in, not by refresh — and
// sign-in with the correct password says email_not_confirmed (403) instead
// of the 401 a wrong password gets. Needs a migrated PG16, like
// TestPasskeyStore_DB: ENDUSER_PASSKEY_TEST_URL.
func TestEmailConfirmation_DB(t *testing.T) {
	url := os.Getenv("ENDUSER_PASSKEY_TEST_URL")
	if url == "" {
		t.Skip("ENDUSER_PASSKEY_TEST_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Skipf("connect: %v", err)
	}
	// Registered first so it runs last: the cleanup below still needs it
	// (a deferred Close runs before t.Cleanup).
	t.Cleanup(pool.Close)

	pid := uuid.NewString()
	ownerEmail := "confirm-" + pid + "@t.local"
	if _, err := pool.Exec(ctx, `INSERT INTO platform_users (email) VALUES ($1)`, ownerEmail); err != nil {
		t.Fatal(err)
	}
	var ownerID, schema string
	_ = pool.QueryRow(ctx, `SELECT id FROM platform_users WHERE email=$1`, ownerEmail).Scan(&ownerID)
	if _, err := pool.Exec(ctx, `INSERT INTO projects (id, owner_id, name, slug, schema_name, s3_bucket, region, plan, status)
		VALUES ($1,$2,'Confirm',$3,'tenant_confirm',$4,'fr-par','free','provisioning')`, pid, ownerID, "confirm-"+pid[:8], "confirm-"+pid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `SELECT provision_tenant($1,'Confirm','free')`, pid); err != nil {
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
	rawConfirm := []byte(`{
		"providers": {"email_password": {"enabled": true}},
		"password_min_length": 8,
		"require_email_confirmation": true,
		"redirect_urls": ["https://app.example.com/verify"],
		"email_verification_url": "https://app.example.com/verify",
		"session_duration": "1h"
	}`)
	confirmCfg := tenant.ParseAuthConfig(rawConfirm)
	if !confirmCfg.RequireEmailConfirmation {
		t.Fatal("test config: require_email_confirmation not parsed")
	}
	svc := &AuthService{pool: pool} // no email service: nothing is sent

	const email, password = "teen@example.com", "correct-horse-1"

	// Sign-up: the user, no tokens.
	resp, err := svc.SignUp(ctx, schema, jwtSecret, pid, confirmCfg, SignUpRequest{Email: email, Password: password})
	if err != nil {
		t.Fatalf("signup: %v", err)
	}
	if !resp.ConfirmationRequired || resp.AccessToken != "" || resp.RefreshToken != "" {
		t.Fatalf("signup must not issue a session while confirmation is required: %+v", resp)
	}
	if resp.User.ID == "" || resp.User.EmailConfirmedAt != nil {
		t.Fatalf("signup user = %+v", resp.User)
	}
	body, _ := json.Marshal(resp)
	if bytes.Contains(body, []byte("access_token")) || !bytes.Contains(body, []byte(`"confirmation_required":true`)) {
		t.Fatalf("signup wire shape: %s", body)
	}

	// Sign-in: correct password → ErrEmailNotConfirmed; wrong → generic.
	if _, err := svc.SignIn(ctx, schema, jwtSecret, pid, confirmCfg, SignInRequest{Email: email, Password: password}); !errors.Is(err, ErrEmailNotConfirmed) {
		t.Fatalf("signin (unconfirmed, right password) = %v, want ErrEmailNotConfirmed", err)
	}
	if _, err := svc.SignIn(ctx, schema, jwtSecret, pid, confirmCfg, SignInRequest{Email: email, Password: "wrong-password"}); err == nil || errors.Is(err, ErrEmailNotConfirmed) {
		t.Fatalf("signin (wrong password) = %v, want the generic error", err)
	}

	// The handler: 403 email_not_confirmed vs 401 for a wrong password.
	pc := &auth.ProjectContext{ProjectID: pid, SchemaName: schema, JWTSecret: jwtSecret, AuthConfig: rawConfirm}
	signin := func(pw string) (int, string) {
		b, _ := json.Marshal(SignInRequest{Email: email, Password: pw})
		r := httptest.NewRequest(http.MethodPost, "/v1/auth/signin", bytes.NewReader(b))
		r = r.WithContext(auth.ContextWithProject(r.Context(), pc))
		w := httptest.NewRecorder()
		HandleSignIn(svc)(w, r)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		msg, _ := out["error"].(string)
		return w.Code, msg
	}
	if code, msg := signin(password); code != http.StatusForbidden || msg != "email_not_confirmed" {
		t.Fatalf("handler, right password: %d %q", code, msg)
	}
	if code, msg := signin("wrong-password"); code != http.StatusUnauthorized || msg != "invalid email or password" {
		t.Fatalf("handler, wrong password: %d %q", code, msg)
	}

	// Refresh: a refresh token held by an unconfirmed user (e.g. issued
	// before this change) gets no new tokens, and stays as it was.
	raw, err := svc.createRefreshToken(ctx, schema, resp.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RefreshToken(ctx, schema, jwtSecret, pid, confirmCfg, raw); !errors.Is(err, ErrEmailNotConfirmed) {
		t.Fatalf("refresh (unconfirmed) = %v, want ErrEmailNotConfirmed", err)
	}

	// Confirm → everything works and the user object says so.
	if _, err := pool.Exec(ctx, `UPDATE `+quoteIdent(schema)+`.users SET email_confirmed_at = now() WHERE id = $1`, resp.User.ID); err != nil {
		t.Fatal(err)
	}
	in, err := svc.SignIn(ctx, schema, jwtSecret, pid, confirmCfg, SignInRequest{Email: email, Password: password})
	if err != nil || in.AccessToken == "" || in.User.EmailConfirmedAt == nil {
		t.Fatalf("signin after confirm: %+v, %v", in, err)
	}
	ref, err := svc.RefreshToken(ctx, schema, jwtSecret, pid, confirmCfg, raw)
	if err != nil || ref.AccessToken == "" || ref.User.EmailConfirmedAt == nil {
		t.Fatalf("refresh after confirm (old token kept by the refused refresh): %+v, %v", ref, err)
	}
	u, err := svc.GetUser(ctx, schema, resp.User.ID)
	if err != nil || u.EmailConfirmedAt == nil {
		t.Fatalf("GetUser: %+v, %v", u, err)
	}

	// Without the setting, sign-up signs the user in straight away.
	openCfg := tenant.ParseAuthConfig([]byte(`{"providers": {"email_password": {"enabled": true}}, "password_min_length": 8, "session_duration": "1h"}`))
	open, err := svc.SignUp(ctx, schema, jwtSecret, pid, openCfg, SignUpRequest{Email: "other@example.com", Password: password})
	if err != nil || open.ConfirmationRequired || open.AccessToken == "" || open.User.EmailConfirmedAt == nil {
		t.Fatalf("signup without confirmation: %+v, %v", open, err)
	}
}
