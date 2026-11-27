package workers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// listOf returns a list text with n generated disposable domains plus the
// given extra lines.
func listOf(n int, extra ...string) string {
	var b strings.Builder
	b.WriteString("# comment\n\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "zz-disp-%05d.example\n", i)
	}
	for _, e := range extra {
		b.WriteString(e + "\n")
	}
	return b.String()
}

func TestParseDisposableList(t *testing.T) {
	got, err := parseDisposableList(strings.NewReader(listOf(disposableMinDomains,
		"MixedCase.Example", "trailing-dot.example.", "co.uk", "com.de", "not a domain", "x_y.example",
		"zz-disp-00001.example")))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	set := map[string]bool{}
	for _, d := range got {
		set[d] = true
	}
	if !set["mixedcase.example"] || !set["trailing-dot.example"] {
		t.Errorf("normalisation: lowercase / trailing dot not applied")
	}
	for _, bad := range []string{"co.uk", "not a domain", "x_y.example"} {
		if set[bad] {
			t.Errorf("%q should be skipped", bad)
		}
	}
	if len(got) != disposableMinDomains+2 {
		t.Errorf("got %d domains, want %d (duplicates and bad entries skipped)", len(got), disposableMinDomains+2)
	}
}

func TestParseDisposableList_Refusals(t *testing.T) {
	cases := map[string]string{
		"protected provider":           listOf(disposableMinDomains, "gmail.com"),
		"parent of a protected domain": listOf(disposableMinDomains, "proton.me", "me"),
		"too few":                      listOf(disposableMinDomains - 1),
	}
	for name, body := range cases {
		if _, err := parseDisposableList(strings.NewReader(body)); err == nil {
			t.Errorf("%s: list accepted, want refusal", name)
		}
	}
	// Single-label entries ("me", "de") never get that far (no dot), but
	// the parent check covers them anyway.
	if p := protectedParentOf("mail.com"); p != "" {
		t.Errorf("mail.com is not a parent of a protected domain, got %q", p)
	}
	if p := protectedParentOf("de"); p == "" {
		t.Errorf("de is a parent of gmx.de")
	}
}

func TestParseDisposableList_TooLarge(t *testing.T) {
	body := strings.Repeat("a", disposableMaxBytes/2) + "\n" + strings.Repeat("b", disposableMaxBytes/2) + "\n"
	if _, err := parseDisposableList(strings.NewReader(body)); err == nil {
		t.Fatal("oversized list accepted")
	}
}

// syncTestPool connects to the local test database (DATABASE_URL, or the
// docker-compose default) and skips when it or migration 000163 is missing.
func syncTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test")
	}
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = "postgres://eurobase_api:localdev@localhost:5433/eurobase?sslmode=disable"
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Skipf("no test database: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("no test database: %v", err)
	}
	var ok bool
	if err := pool.QueryRow(ctx,
		`SELECT to_regclass('public.platform_blocked_domain_exclusions') IS NOT NULL`).Scan(&ok); err != nil || !ok {
		pool.Close()
		t.Skip("migration 000163 not applied in the test database")
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestDisposableDomainSync runs the whole sync against a test server and
// the test database: adds list rows, keeps manual rows, respects
// exclusions, removes dropped rows, and refuses a list that would remove
// too many.
func TestDisposableDomainSync(t *testing.T) {
	pool := syncTestPool(t)
	ctx := context.Background()
	clean := func() {
		_, _ = pool.Exec(ctx, `DELETE FROM public.platform_blocked_signup_domains WHERE domain LIKE 'zz-disp-%' OR domain LIKE 'zz-manual-%'`)
		_, _ = pool.Exec(ctx, `DELETE FROM public.platform_blocked_domain_exclusions WHERE domain LIKE 'zz-disp-%'`)
	}
	// The test owns all disposable_list rows while it runs.
	var foreign int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM public.platform_blocked_signup_domains
		WHERE source = 'disposable_list' AND domain NOT LIKE 'zz-disp-%'`).Scan(&foreign)
	if foreign > 0 {
		t.Skip("test database already has disposable_list rows")
	}
	clean()
	t.Cleanup(clean)

	body := listOf(disposableMinDomains + 20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	// A manual row that is also on the list, and an exclusion.
	if _, err := pool.Exec(ctx, `INSERT INTO public.platform_blocked_signup_domains (domain, reason, source)
		VALUES ('zz-disp-00000.example', 'by hand', 'manual'), ('zz-manual-1.example', 'by hand', 'manual')`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO public.platform_blocked_domain_exclusions (domain) VALUES ('zz-disp-00001.example')`); err != nil {
		t.Fatal(err)
	}

	res, err := syncDisposableDomains(ctx, pool, srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if res.Added != disposableMinDomains+20-2 || res.Removed != 0 {
		t.Fatalf("first sync: %+v, want added %d", res, disposableMinDomains+20-2)
	}
	var src string
	if err := pool.QueryRow(ctx, `SELECT source FROM public.platform_blocked_signup_domains WHERE domain = 'zz-disp-00000.example'`).Scan(&src); err != nil || src != "manual" {
		t.Fatalf("manual row changed: source %q, err %v", src, err)
	}
	var excluded int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM public.platform_blocked_signup_domains WHERE domain = 'zz-disp-00001.example'`).Scan(&excluded)
	if excluded != 0 {
		t.Fatal("excluded domain was added")
	}

	// Upstream drops the last 10 and adds 3: within limits.
	body = listOf(disposableMinDomains+10, "zz-disp-new-1.example", "zz-disp-new-2.example", "zz-disp-new-3.example")
	res, err = syncDisposableDomains(ctx, pool, srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if res.Added != 3 || res.Removed != 10 {
		t.Fatalf("second sync: %+v, want added 3 removed 10", res)
	}
	var manualKept int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM public.platform_blocked_signup_domains WHERE domain LIKE 'zz-manual-%'`).Scan(&manualKept)
	if manualKept != 1 {
		t.Fatal("manual row removed by the sync")
	}

	// Upstream replaces most of the list (would remove > 10%): refused,
	// nothing changes.
	var before int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM public.platform_blocked_signup_domains WHERE source = 'disposable_list'`).Scan(&before)
	body = strings.Replace(listOf(disposableMinDomains), "zz-disp-0", "zz-disp-x", -1)
	if _, err := syncDisposableDomains(ctx, pool, srv.Client(), srv.URL); err == nil {
		t.Fatal("rewritten list accepted")
	}
	var after int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM public.platform_blocked_signup_domains WHERE source = 'disposable_list'`).Scan(&after)
	if before != after {
		t.Fatalf("refused sync changed the list: %d → %d", before, after)
	}

	// Bad HTTP status: refused.
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) })
	if _, err := syncDisposableDomains(ctx, pool, srv.Client(), srv.URL); err == nil {
		t.Fatal("404 accepted")
	}
}
