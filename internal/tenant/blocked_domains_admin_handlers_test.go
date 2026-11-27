package tenant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
)

func TestBlockedDomainsQuery_Clamps(t *testing.T) {
	cases := []struct {
		url           string
		q             string
		limit, offset int
	}{
		{"/x", "", blockedDomainsDefaultLimit, 0},
		{"/x?limit=10&offset=20", "", 10, 20},
		{"/x?limit=100000", "", blockedDomainsMaxLimit, 0},
		{"/x?limit=-5&offset=-3", "", blockedDomainsDefaultLimit, 0},
		{"/x?limit=abc&offset=xyz", "", blockedDomainsDefaultLimit, 0},
		{"/x?q=%20MailBox.ORG%20", "mailbox.org", blockedDomainsDefaultLimit, 0},
		{"/x?q=a%FFb%00c", "abc", blockedDomainsDefaultLimit, 0},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, c.url, nil)
		q, limit, offset := blockedDomainsQuery(r)
		if q != c.q || limit != c.limit || offset != c.offset {
			t.Errorf("%s: got (%q, %d, %d), want (%q, %d, %d)", c.url, q, limit, offset, c.q, c.limit, c.offset)
		}
	}
}

func TestBlockedDomainsQuery_CutsOnRuneBoundary(t *testing.T) {
	long := strings.Repeat("a", 252) + "é" + "x"
	r := httptest.NewRequest(http.MethodGet, "/x?q="+url.QueryEscape(long), nil)
	q, _, _ := blockedDomainsQuery(r)
	if !utf8.ValidString(q) || utf8.RuneCountInString(q) != 253 {
		t.Fatalf("q not cut on a rune boundary: valid=%v runes=%d", utf8.ValidString(q), utf8.RuneCountInString(q))
	}
}

func TestEscapeLikePattern(t *testing.T) {
	if got := escapeLikePattern(`a_b%c\d`); got != `a\_b\%c\\d` {
		t.Fatalf("got %q", got)
	}
}

// TestAdminListBlockedDomains_PaginatesAndFilters runs against the test
// database: pages don't overlap, total counts the matching rows, and the
// search is a literal substring (an underscore is not a wildcard).
func TestAdminListBlockedDomains_PaginatesAndFilters(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()
	var exists bool
	if err := pool.QueryRow(ctx,
		`SELECT to_regclass('public.platform_blocked_signup_domains') IS NOT NULL`).Scan(&exists); err != nil || !exists {
		t.Skip("platform_blocked_signup_domains not migrated in the test database")
	}

	const prefix = "zz-pagetest-"
	cleanup := func() {
		_, _ = pool.Exec(ctx, `DELETE FROM public.platform_blocked_signup_domains WHERE domain LIKE 'zz-pagetest-%'`)
	}
	cleanup()
	t.Cleanup(cleanup)
	for i := 0; i < 5; i++ {
		if _, err := pool.Exec(ctx,
			`INSERT INTO public.platform_blocked_signup_domains (domain, reason) VALUES ($1, 'test')`,
			fmt.Sprintf("%s%d.example", prefix, i)); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	// "zz-pagetestx..." must not match a search for "zz-pagetest_": the
	// underscore is literal.
	if _, err := pool.Exec(ctx,
		`INSERT INTO public.platform_blocked_signup_domains (domain, reason) VALUES ('zz-pagetest-x.example', 'test')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	type page struct {
		Entries []BlockedDomainEntry `json:"entries"`
		Total   int                  `json:"total"`
		Limit   int                  `json:"limit"`
		Offset  int                  `json:"offset"`
	}
	get := func(query string) page {
		t.Helper()
		rec := httptest.NewRecorder()
		AdminListBlockedDomains(pool).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/blocked-domains?"+query, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d: %s", query, rec.Code, rec.Body.String())
		}
		var p page
		if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return p
	}

	p1 := get("q=" + prefix + "&limit=4&offset=0")
	p2 := get("q=" + prefix + "&limit=4&offset=4")
	if p1.Total != 6 || p2.Total != 6 {
		t.Fatalf("total = %d / %d, want 6", p1.Total, p2.Total)
	}
	if len(p1.Entries) != 4 || len(p2.Entries) != 2 {
		t.Fatalf("page sizes = %d, %d, want 4, 2", len(p1.Entries), len(p2.Entries))
	}
	seen := map[string]bool{}
	for _, e := range append(p1.Entries, p2.Entries...) {
		if seen[e.Domain] {
			t.Fatalf("domain %s on both pages", e.Domain)
		}
		seen[e.Domain] = true
	}

	if p := get("q=zz-pagetest_"); p.Total != 0 {
		t.Fatalf("underscore treated as a wildcard: total %d", p.Total)
	}
	if p := get("q=ZZ-PAGETEST-X"); p.Total != 1 || p.Entries[0].Domain != "zz-pagetest-x.example" {
		t.Fatalf("case-insensitive search: %+v", p)
	}
}

// TestBlockedDomains_UnblockListDomainExcludes: unblocking a domain that
// came from the disposable list records an exclusion (the weekly sync
// won't re-add it); blocking it by hand again clears the exclusion and
// makes the row manual.
func TestBlockedDomains_UnblockListDomainExcludes(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()
	var ok bool
	if err := pool.QueryRow(ctx,
		`SELECT to_regclass('public.platform_blocked_domain_exclusions') IS NOT NULL`).Scan(&ok); err != nil || !ok {
		t.Skip("migration 000163 not applied in the test database")
	}
	const d = "zz-unblock-test.example"
	clean := func() {
		_, _ = pool.Exec(ctx, `DELETE FROM public.platform_blocked_signup_domains WHERE domain = $1`, d)
		_, _ = pool.Exec(ctx, `DELETE FROM public.platform_blocked_domain_exclusions WHERE domain = $1`, d)
	}
	clean()
	t.Cleanup(clean)
	if _, err := pool.Exec(ctx, `INSERT INTO public.platform_blocked_signup_domains (domain, reason, source)
		VALUES ($1, 'list', 'disposable_list')`, d); err != nil {
		t.Fatal(err)
	}

	r := chi.NewRouter()
	r.Delete("/blocked-domains/{domain}", AdminRemoveBlockedDomain(pool))
	r.Post("/blocked-domains", AdminAddBlockedDomain(pool))

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/blocked-domains/"+d, nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("unblock: status %d: %s", rec.Code, rec.Body.String())
	}
	var excluded bool
	_ = pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM public.platform_blocked_domain_exclusions WHERE domain = $1)`, d).Scan(&excluded)
	if !excluded {
		t.Fatal("unblocking a list domain recorded no exclusion")
	}

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/blocked-domains", strings.NewReader(`{"domain":"`+d+`"}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("block: status %d: %s", rec.Code, rec.Body.String())
	}
	var source string
	_ = pool.QueryRow(ctx, `SELECT source FROM public.platform_blocked_signup_domains WHERE domain = $1`, d).Scan(&source)
	_ = pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM public.platform_blocked_domain_exclusions WHERE domain = $1)`, d).Scan(&excluded)
	if source != "manual" || excluded {
		t.Fatalf("after manual block: source %q, excluded %v; want manual, false", source, excluded)
	}

	// Unblocking a manual row records no exclusion.
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/blocked-domains/"+d, nil))
	_ = pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM public.platform_blocked_domain_exclusions WHERE domain = $1)`, d).Scan(&excluded)
	if rec.Code != http.StatusNoContent || excluded {
		t.Fatalf("unblock manual: status %d, excluded %v", rec.Code, excluded)
	}
}
