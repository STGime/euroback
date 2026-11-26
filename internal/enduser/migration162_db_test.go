package enduser

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/eurobase/euroback/internal/locale"
	"github.com/eurobase/euroback/internal/tenant"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Runs the project updates of migration 000162 (taken from the file) on
// seeded projects inside a rolled-back transaction: a project that never
// saved auth settings keeps the localhost defaults for every gateway
// version, a saved config only gains email_locale, and an existing
// project's emails stay English even for a German browser.
func TestMigration162ProjectUpdates_DB(t *testing.T) {
	url := os.Getenv("ENDUSER_PASSKEY_TEST_URL")
	if url == "" {
		t.Skip("ENDUSER_PASSKEY_TEST_URL not set")
	}
	raw, err := os.ReadFile("../../migrations/000162_user_email_locale.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	i := strings.Index(src, "UPDATE public.projects\n   SET auth_config = '{\"providers\"")
	if i < 0 {
		t.Fatal("project updates not found in 000162")
	}
	updates := src[i:]

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Skipf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	owner := uuid.NewString()
	if _, err := tx.Exec(ctx, `INSERT INTO platform_users (id, email) VALUES ($1, $2)`, owner, "m162-"+owner+"@t.local"); err != nil {
		t.Fatal(err)
	}
	seed := map[string]*string{ // name -> auth_config (nil = SQL NULL)
		"empty":   strPtr(`{}`),
		"sqlnull": nil,
		"jsnull":  strPtr(`null`),
		"saved":   strPtr(`{"redirect_urls":["https://app.example.com"],"cors_origins":[],"password_min_length":10}`),
		"chosen":  strPtr(`{"email_locale":"de","redirect_urls":["https://x.example"]}`),
	}
	ids := map[string]string{}
	for name, cfg := range seed {
		id := uuid.NewString()
		ids[name] = id
		if _, err := tx.Exec(ctx, `INSERT INTO projects (id, owner_id, name, slug, schema_name, s3_bucket, region, plan, status, auth_config)
			VALUES ($1,$2,$3,$4,$5,$6,'fr-par','pro','active',$7::jsonb)`,
			id, owner, "m162 "+name, "m162-"+id[:8], "tenant_m162_"+id[:8], "m162-"+id, cfg); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	if _, err := tx.Exec(ctx, updates); err != nil {
		t.Fatalf("000162 project updates: %v", err)
	}
	read := func(name string) []byte {
		var b []byte
		if err := tx.QueryRow(ctx, `SELECT auth_config::text FROM projects WHERE id=$1`, ids[name]).Scan(&b); err != nil {
			t.Fatal(err)
		}
		return b
	}

	for _, name := range []string{"empty", "sqlnull", "jsnull"} {
		stored := read(name)
		cfg := tenant.ParseAuthConfig(stored)
		if cfg.EmailLocale != "en" || !cfg.IsCORSOriginAllowed("http://localhost:3000") || len(cfg.RedirectURLs) != 1 || cfg.RedirectURLs[0] != "http://localhost:3000" {
			t.Errorf("%s: stored %s -> %+v, want the defaults plus en", name, stored, cfg)
		}
		if !strings.Contains(string(stored), `"cors_origins": ["http://localhost:3000"]`) {
			t.Errorf("%s: defaults must be written out for older gateways: %s", name, stored)
		}
	}
	saved := tenant.ParseAuthConfig(read("saved"))
	if saved.EmailLocale != "en" || saved.PasswordMinLength != 10 || len(saved.RedirectURLs) != 1 || saved.IsCORSOriginAllowed("http://localhost:3000") {
		t.Errorf("saved config must only gain email_locale: %+v", saved)
	}
	if chosen := tenant.ParseAuthConfig(read("chosen")); chosen.EmailLocale != "de" {
		t.Errorf("an existing choice must be kept, got %q", chosen.EmailLocale)
	}

	// The deploy case: an existing project, an app that doesn't send a
	// locale, a German browser: nothing is stored and the email is English.
	h := locale.WithAcceptLanguage(ctx, "de-DE,de;q=0.9")
	if l := locale.ForNewUser(h, saved.EmailLocale); l != "" {
		t.Errorf("existing project: the browser's language must not be stored, got %q", l)
	}
	if l := locale.Resolve("", locale.HintsFrom(h).Requested, locale.HintsFrom(h).AcceptLanguage, saved.EmailLocale); l != "en" {
		t.Errorf("existing project, German browser: email language %q, want en", l)
	}
}

func strPtr(s string) *string { return &s }
