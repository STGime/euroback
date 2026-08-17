package storage

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// Regression coverage for the URL-encoding bug traced to a support
// email from 2026-08-17 (delete of `ChatGPT Image 28. Okt. 2025,
// 12_13_05.png` returned "Not found"). PR #421 review caught that an
// unconditional PathUnescape was a strict-worse fix (double-decoded
// filenames containing a literal `%HH`), so the extractor now gates
// on `r.URL.RawPath` — mirroring chi's own routing branch.
//
// **Tests are split into two groups.** The mutation test the
// reviewer ran (disable the decode = pre-fix behaviour) proved that
// most "non-ASCII" cases were vacuous: chi already returns pure
// non-ASCII paths decoded (empty `RawPath`), so those subtests would
// stay green even if the fix were removed — they gave false
// confidence.
//
//   * `TestExtractWildcardKey_ReservedCharsMustDecode` covers
//     filenames chi returns ENCODED (RawPath is set). These are the
//     genuine bug-catchers. Comma is Bertram's second file.
//   * `TestExtractWildcardKey_LiteralPercentMustNotDoubleDecode` is
//     the regression guard for the review-caught fix bug. Filenames
//     containing a literal `%20` (etc.) must survive unmangled.
//   * `TestExtractWildcardKey_AlreadyDecodedControls` covers the
//     "chi returns decoded, we must not touch it" side. These would
//     stay green even without the fix — they're kept only as a
//     positive contract on the extractor's output shape, NOT as
//     bug-catchers.

// Reserved characters (RFC 3986 sub-delims + a few) that Go's
// default path-escaping doesn't produce — when the client encodes
// them, Go populates `RawPath` and chi returns the wildcard
// encoded. These are the genuine failure class that motivated the
// fix. Mutation-verified: removing the decode fails every subtest
// here.
func TestExtractWildcardKey_ReservedCharsMustDecode(t *testing.T) {
	cases := []struct {
		name       string
		clientKey  string
		encodedURL string
	}{
		{
			name:       "comma (Bertram's ChatGPT export file)",
			clientKey:  "Screenshot 2026-08-17, 14_30.png",
			encodedURL: "/storage/Screenshot%202026-08-17%2C%2014_30.png",
		},
		{
			name:       "plus sign",
			clientKey:  "invoice+2026.pdf",
			encodedURL: "/storage/invoice%2B2026.pdf",
		},
		{
			name:       "hash sign",
			clientKey:  "issue-#42-notes.md",
			encodedURL: "/storage/issue-%2342-notes.md",
		},
		{
			name:       "semicolon and ampersand",
			clientKey:  "a;b&c.txt",
			encodedURL: "/storage/a%3Bb%26c.txt",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := runWildcardRoute(t, tc.encodedURL)
			if got != tc.clientKey {
				t.Errorf("key mismatch:\n  extracted: %q\n  wanted:    %q\n  URL sent:  %s",
					got, tc.clientKey, tc.encodedURL)
			}
		})
	}
}

// The review-caught regression class. A filename that legitimately
// contains a literal `%HH` sequence (e.g. `report_50%20pct.pdf`)
// gets double-URL-encoded on the wire (`%25` → `%`, then `%20` →
// ` `). Under the old unconditional decode, extract would return
// `report_50 pct.pdf` and every lookup on the real key would 404.
//
// This is a genuine bug-catcher: mutation-verified against the
// unconditional-decode variant (all subtests fail there).
func TestExtractWildcardKey_LiteralPercentMustNotDoubleDecode(t *testing.T) {
	cases := []struct {
		name       string
		clientKey  string
		encodedURL string
	}{
		{
			name:       "literal %20 in name",
			clientKey:  "report_50%20pct.pdf",
			encodedURL: "/storage/report_50%2520pct.pdf",
		},
		{
			name:       "literal %41 in name (would double-decode to A)",
			clientKey:  "a%41b.txt",
			encodedURL: "/storage/a%2541b.txt",
		},
		{
			name:       "URL-safe base64 with padding literal",
			clientKey:  "checksum-abc%3D%3D.bin",
			encodedURL: "/storage/checksum-abc%253D%253D.bin",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := runWildcardRoute(t, tc.encodedURL)
			if got != tc.clientKey {
				t.Errorf("double-decode regression:\n  extracted: %q\n  wanted:    %q\n  URL sent:  %s",
					got, tc.clientKey, tc.encodedURL)
			}
		})
	}
}

// Filenames where chi returns the wildcard already-decoded (empty
// `RawPath`). Kept only as a positive contract on the extractor's
// output shape — these subtests would stay green even without the
// fix, so they are NOT bug-catchers on their own. Labelled
// explicitly so a future reader doesn't misread them as guards.
func TestExtractWildcardKey_AlreadyDecodedControls(t *testing.T) {
	cases := []struct {
		name       string
		clientKey  string
		encodedURL string
	}{
		{
			name:       "german umlaut (Bertram's first file)",
			clientKey:  "Blätter.jpg",
			encodedURL: "/storage/Bl%C3%A4tter.jpg",
		},
		{
			name:       "emoji (4-byte UTF-8)",
			clientKey:  "🚀-launch.pdf",
			encodedURL: "/storage/%F0%9F%9A%80-launch.pdf",
		},
		{
			name:       "cyrillic",
			clientKey:  "Привет-мир.txt",
			encodedURL: "/storage/%D0%9F%D1%80%D0%B8%D0%B2%D0%B5%D1%82-%D0%BC%D0%B8%D1%80.txt",
		},
		{
			name:       "ascii-only unchanged",
			clientKey:  "hello.txt",
			encodedURL: "/storage/hello.txt",
		},
		{
			name:       "nested path preserves slashes",
			clientKey:  "avatars/Björn.png",
			encodedURL: "/storage/avatars/Bj%C3%B6rn.png",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := runWildcardRoute(t, tc.encodedURL)
			if got != tc.clientKey {
				t.Errorf("extractor mangled a value chi already decoded:\n  extracted: %q\n  wanted:    %q\n  URL sent:  %s",
					got, tc.clientKey, tc.encodedURL)
			}
		})
	}
}

// Security half of the fix: decode-BEFORE-validate closes a
// traversal gap. Pre-fix, `ValidateStorageKey` ran on the encoded
// string so `%2E%2E%2Fetc%2Fpasswd` slipped past the `..`-segment
// check (`strings.Split` on `/` never saw literal `..`). Post-fix
// the extract decodes first (`RawPath` is set for these inputs —
// `%2E` isn't produced by default escape), the check sees `..`, and
// rejects.
//
// chi's own routing blocks upstream traversal; this is the belt on
// top. Mutation-verified: reverting the decode makes both subtests
// fail (extractor returns the still-encoded key, validate accepts
// it).
func TestExtractWildcardKey_TraversalCaughtAfterDecode(t *testing.T) {
	cases := []string{
		"%2E%2E%2Fetc%2Fpasswd",      // ../etc/passwd
		"avatars%2F%2E%2E%2Fsecrets", // avatars/../secrets
	}
	for _, encoded := range cases {
		t.Run(encoded, func(t *testing.T) {
			extracted := runWildcardRoute(t, "/storage/"+encoded)
			if !strings.Contains(extracted, "..") {
				t.Fatalf("expected decoded key to contain literal '..'; got %q — decode-first ordering broken", extracted)
			}
			if err := ValidateStorageKey(extracted); err == nil {
				t.Errorf("ValidateStorageKey accepted decoded traversal key %q (must reject '..' segment)", extracted)
			}
		})
	}
}

// runWildcardRoute wires a chi router that mirrors the production
// storage mount (Delete + Get on `/*`), fires the request through
// real chi routing (not a synthetic RouteContext), and returns
// whatever extractWildcardKey observed. Using httptest.NewRequest
// with a string URL preserves r.URL.RawPath when the input contains
// percent-escapes — the exact code path chi's routeHTTP branches
// on (mux.go:433-434).
func runWildcardRoute(t *testing.T, encodedURL string) string {
	t.Helper()
	var got string
	r := chi.NewRouter()
	r.Route("/storage", func(r chi.Router) {
		handler := func(w http.ResponseWriter, req *http.Request) {
			got = extractWildcardKey(req)
			w.WriteHeader(http.StatusNoContent)
		}
		r.Delete("/*", handler)
		r.Get("/*", handler)
	})
	req := httptest.NewRequest(http.MethodDelete, encodedURL, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("route did not match %s — status %d, body %q", encodedURL, w.Code, w.Body.String())
	}
	return got
}
