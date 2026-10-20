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
	"github.com/eurobase/euroback/internal/sms"
	"github.com/eurobase/euroback/internal/tenant"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

// With auth_config.disable_signup, public signup creates no user (password
// signup answers signups_disabled; phone OTP for an unknown number answers
// as if sent, without creating one), while an existing user still signs
// in. Needs a migrated PG16, like TestEmailConfirmation_DB.
func TestDisableSignup_DB(t *testing.T) {
	url := os.Getenv("ENDUSER_PASSKEY_TEST_URL")
	if url == "" {
		t.Skip("ENDUSER_PASSKEY_TEST_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Skipf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	pid := uuid.NewString()
	ownerEmail := "nosignup-" + pid + "@t.local"
	if _, err := pool.Exec(ctx, `INSERT INTO platform_users (email) VALUES ($1)`, ownerEmail); err != nil {
		t.Fatal(err)
	}
	var ownerID, schema string
	_ = pool.QueryRow(ctx, `SELECT id FROM platform_users WHERE email=$1`, ownerEmail).Scan(&ownerID)
	if _, err := pool.Exec(ctx, `INSERT INTO projects (id, owner_id, name, slug, schema_name, s3_bucket, region, plan, status)
		VALUES ($1,$2,'NoSignup',$3,'tenant_nosignup',$4,'fr-par','pro','provisioning')`, pid, ownerID, "nosignup-"+pid[:8], "nosignup-"+pid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `SELECT provision_tenant($1,'NoSignup','pro')`, pid); err != nil {
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
	raw := []byte(`{
		"providers": {"email_password": {"enabled": true}, "phone": {"enabled": true}},
		"password_min_length": 8,
		"redirect_urls": ["https://app.example.com"],
		"session_duration": "1h",
		"disable_signup": true
	}`)
	cfg := tenant.ParseAuthConfig(raw)
	if !cfg.DisableSignup {
		t.Fatal("test config: disable_signup not parsed")
	}
	svc := &AuthService{pool: pool}
	svc.smsService = sms.NewService(sms.NewClient("", ""), pool) // unconfigured: never sends

	countUsers := func() int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+quoteIdent(schema)+`.users`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Password signup: refused, nothing created.
	if _, err := svc.SignUp(ctx, schema, jwtSecret, pid, cfg, SignUpRequest{Email: "kid@example.com", Password: "correct-horse-1"}); !errors.Is(err, ErrSignupsDisabled) {
		t.Fatalf("signup: want ErrSignupsDisabled, got %v", err)
	}
	if n := countUsers(); n != 0 {
		t.Fatalf("signup created %d users", n)
	}

	// The HTTP answer is 403 signups_disabled.
	body, _ := json.Marshal(map[string]string{"email": "kid2@example.com", "password": "correct-horse-1"})
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/signup", bytes.NewReader(body))
	req = req.WithContext(auth.ContextWithProject(req.Context(), &auth.ProjectContext{
		ProjectID: pid, SchemaName: schema, JWTSecret: jwtSecret, AuthConfig: raw,
	}))
	rec := httptest.NewRecorder()
	HandleSignUp(svc)(rec, req)
	if rec.Code != http.StatusForbidden || !bytes.Contains(rec.Body.Bytes(), []byte(`"signups_disabled"`)) {
		t.Fatalf("signup handler: got %d %s", rec.Code, rec.Body.String())
	}

	// Phone OTP for an unknown number: no error (no enumeration), no user.
	if err := svc.SendPhoneOTP(ctx, schema, pid, "+33612345678", cfg); err != nil {
		t.Fatalf("phone otp: want nil, got %v", err)
	}
	if n := countUsers(); n != 0 {
		t.Fatalf("phone otp created %d users", n)
	}

	// A user created by the developer (Admin API / import) still signs in.
	hash, _ := bcrypt.GenerateFromPassword([]byte("correct-horse-1"), bcrypt.MinCost)
	if _, err := pool.Exec(ctx, `INSERT INTO `+quoteIdent(schema)+`.users (email, password_hash, email_confirmed_at) VALUES ($1, $2, now())`,
		"invited@example.com", string(hash)); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SignIn(ctx, schema, jwtSecret, pid, cfg, SignInRequest{Email: "invited@example.com", Password: "correct-horse-1"}); err != nil {
		t.Fatalf("existing user sign-in: %v", err)
	}
}
