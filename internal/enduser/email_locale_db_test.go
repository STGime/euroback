package enduser

import (
	"context"
	"os"
	"testing"

	"github.com/eurobase/euroback/internal/email"
	"github.com/eurobase/euroback/internal/locale"
	"github.com/eurobase/euroback/internal/tenant"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Sign-up stores the user's language (the app's locale, else
// Accept-Language), and the auth emails then use it ahead of the
// request's language and the project default.
func TestEmailLocale_DB(t *testing.T) {
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
	ownerEmail := "locale-" + pid + "@t.local"
	if _, err := pool.Exec(ctx, `INSERT INTO platform_users (email) VALUES ($1)`, ownerEmail); err != nil {
		t.Fatal(err)
	}
	var ownerID, schema string
	_ = pool.QueryRow(ctx, `SELECT id FROM platform_users WHERE email=$1`, ownerEmail).Scan(&ownerID)
	if _, err := pool.Exec(ctx, `INSERT INTO projects (id, owner_id, name, slug, schema_name, s3_bucket, region, plan, status, auth_config)
		VALUES ($1,$2,'Locale',$3,'tenant_locale',$4,'fr-par','free','provisioning','{}')`, pid, ownerID, "locale-"+pid[:8], "locale-"+pid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `SELECT provision_tenant($1,'Locale','free')`, pid); err != nil {
		t.Fatal(err)
	}
	_ = pool.QueryRow(ctx, `SELECT schema_name FROM projects WHERE id=$1`, pid).Scan(&schema)
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `SELECT deprovision_tenant($1)`, pid)
		_, _ = pool.Exec(c, `DELETE FROM projects WHERE id=$1`, pid)
		_, _ = pool.Exec(c, `DELETE FROM platform_users WHERE id=$1`, ownerID)
	})

	auto := tenant.ParseAuthConfig([]byte(`{"providers":{"email_password":{"enabled":true}},"password_min_length":8,"session_duration":"1h"}`))
	dutch := auto
	dutch.EmailLocale = "nl"
	svc := &AuthService{pool: pool}
	const jwtSecret = "test-jwt-secret-0123456789abcdef0123456789"

	signUp := func(ctx context.Context, cfg tenant.AuthConfig, addr string) string {
		resp, err := svc.SignUp(ctx, schema, jwtSecret, pid, cfg, SignUpRequest{Email: addr, Password: "correct-horse-1"})
		if err != nil {
			t.Fatalf("signup %s: %v", addr, err)
		}
		return resp.User.ID
	}
	stored := func(id string) string {
		var l *string
		if err := pool.QueryRow(ctx, `SELECT locale FROM `+schema+`.users WHERE id=$1`, id).Scan(&l); err != nil {
			t.Fatal(err)
		}
		if l == nil {
			return ""
		}
		return *l
	}

	// Project on "automatic" (no default language).
	requested := signUp(locale.WithAcceptLanguage(locale.WithRequested(ctx, "de-AT"), "sv"), auto, "a@example.com")
	header := signUp(locale.WithAcceptLanguage(ctx, "it-IT, sv;q=0.8"), auto, "b@example.com")
	none := signUp(locale.WithRequested(ctx, "it"), auto, "c@example.com")
	// Project with an explicit default: the browser's language isn't stored.
	explicit := signUp(locale.WithAcceptLanguage(ctx, "sv"), dutch, "d@example.com")
	explicitRequested := signUp(locale.WithAcceptLanguage(locale.WithRequested(ctx, "fr"), "sv"), dutch, "e@example.com")
	for id, want := range map[string]string{requested: "de", header: "sv", none: "", explicit: "", explicitRequested: "fr"} {
		if got := stored(id); got != want {
			t.Errorf("stored language of %s: %q, want %q", id, got, want)
		}
	}

	es := email.NewEmailService(nil, pool, "")
	lang := func(ctx context.Context, id string) string { return es.Locale(ctx, pid, schema, id) }
	setDefault := func(l string) {
		if _, err := pool.Exec(ctx, `UPDATE projects SET auth_config = jsonb_build_object('email_locale', $2::text) WHERE id = $1`, pid, l); err != nil {
			t.Fatal(err)
		}
	}

	// Automatic.
	if got := lang(locale.WithRequested(ctx, "fr"), requested); got != "de" {
		t.Errorf("stored language must win over the request: got %q", got)
	}
	if got := lang(locale.WithAcceptLanguage(ctx, "es-ES"), none); got != "es" {
		t.Errorf("automatic, no stored language: the browser's language should apply, got %q", got)
	}
	if got := lang(ctx, none); got != "en" {
		t.Errorf("automatic, nothing known: got %q, want en", got)
	}

	// Explicit default: beats the browser, not the stored or requested language.
	setDefault("nl")
	if got := lang(locale.WithAcceptLanguage(ctx, "es-ES"), none); got != "nl" {
		t.Errorf("explicit default must beat the browser: got %q", got)
	}
	if got := lang(locale.WithRequested(ctx, "fr"), none); got != "fr" {
		t.Errorf("the app's request must beat the explicit default: got %q", got)
	}
	if got := lang(locale.WithAcceptLanguage(ctx, "es"), requested); got != "de" {
		t.Errorf("stored language must beat the explicit default: got %q", got)
	}
	if got := lang(ctx, uuid.NewString()); got != "nl" {
		t.Errorf("unknown user: got %q, want the project default", got)
	}
}
