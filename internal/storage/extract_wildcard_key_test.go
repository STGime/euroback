package storage

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// TestExtractWildcardKey_DecodesPercentEscapes is the regression test
// for the "Not found on delete" support ticket from 2026-08-17
// (Bertram Bollow). The console URL-encodes each path segment before
// calling `DELETE /platform/projects/{id}/storage/<key>`, chi routes
// on r.URL.RawPath and returns the wildcard still-encoded, and the
// downstream DB lookup ran with the encoded key against a row keyed
// on the decoded name — always missing, always 404.
//
// The test drives real HTTP + real chi routing (not just a manual
// chi.RouteContext) so any future chi upgrade that changes the
// URLParam decode contract fails this test loudly rather than
// silently re-introducing the bug.
//
// Filenames picked to cover the failure classes reported in the
// wild plus common-in-practice patterns unrelated to the reporter's
// files:
//   - German umlaut (Bertram's case, kept as a sanity check for the
//     originating scenario)
//   - Spaces + comma + period cluster (matches Bertram's second file)
//   - Emoji (4-byte UTF-8)
//   - French accents (é, à, ç)
//   - Nested path (`/`) — must survive segment-wise encoding
//   - Reserved chars (`#`, `?`, `+`) that URL-encode to different
//     bytes
//   - Cyrillic (multi-byte, non-Latin script)
func TestExtractWildcardKey_DecodesPercentEscapes(t *testing.T) {
	cases := []struct {
		name       string
		clientKey  string // key the client (console/SDK) intends to reference
		encodedURL string // what the client actually sends on the wire
	}{
		{
			name:       "german umlaut (regression: Bertram Blätter.jpg)",
			clientKey:  "Blätter.jpg",
			encodedURL: "/storage/Bl%C3%A4tter.jpg",
		},
		{
			name:       "spaces + comma + period",
			clientKey:  "Screenshot 2026-08-17, 14_30.png",
			encodedURL: "/storage/Screenshot%202026-08-17%2C%2014_30.png",
		},
		{
			name:       "emoji filename (4-byte UTF-8)",
			clientKey:  "🚀-launch.pdf",
			encodedURL: "/storage/%F0%9F%9A%80-launch.pdf",
		},
		{
			name:       "french accents",
			clientKey:  "résumé-François.docx",
			encodedURL: "/storage/r%C3%A9sum%C3%A9-Fran%C3%A7ois.docx",
		},
		{
			name:       "nested path preserves slashes",
			clientKey:  "avatars/Björn.png",
			encodedURL: "/storage/avatars/Bj%C3%B6rn.png",
		},
		{
			name:       "reserved chars: hash / plus",
			clientKey:  "issue-#42+notes.md",
			encodedURL: "/storage/issue-%2342%2Bnotes.md",
		},
		{
			name:       "cyrillic script",
			clientKey:  "Привет-мир.txt",
			encodedURL: "/storage/%D0%9F%D1%80%D0%B8%D0%B2%D0%B5%D1%82-%D0%BC%D0%B8%D1%80.txt",
		},
		{
			name:       "ascii-only unchanged (control case)",
			clientKey:  "hello.txt",
			encodedURL: "/storage/hello.txt",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Wire a chi router that mirrors the production storage
			// mount (Delete + Get on `/*`). The handler records what
			// extractWildcardKey returned so the test can assert.
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

			// httptest.NewRequest constructs the request in a way that
			// preserves r.URL.RawPath when the input has percent-
			// escapes — the exact code path chi's routeHTTP branches
			// on. Using a plain string here (rather than constructing
			// r.URL manually) is what makes the test end-to-end.
			req := httptest.NewRequest(http.MethodDelete, tc.encodedURL, nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusNoContent {
				t.Fatalf("route did not match — status %d, body %q", w.Code, w.Body.String())
			}
			if got != tc.clientKey {
				t.Errorf("key mismatch:\n  extracted: %q\n  wanted:    %q\n  URL sent:  %s",
					got, tc.clientKey, tc.encodedURL)
			}
		})
	}
}

// TestExtractWildcardKey_TraversalCaughtAfterDecode is the security
// half of the fix. Pre-fix, ValidateStorageKey ran on the still-
// encoded wildcard, so a caller sending `%2E%2E%2Fetc%2Fpasswd`
// (`../etc/passwd` encoded) slipped past the `..`-segment check
// because "%2E%2E" isn't literally ".." to strings.Split. Post-fix
// the decode happens first, so validation sees `../etc/passwd` and
// rejects it.
//
// The chi router path guards against upstream path traversal (it
// won't match a route past `..`), but ValidateStorageKey is the
// belt on top of it — this test locks in that belt.
func TestExtractWildcardKey_TraversalCaughtAfterDecode(t *testing.T) {
	// Decode-then-validate: the fix's ordering. If validate ran on
	// the encoded form, `%2E%2E%2Fetc%2Fpasswd` would pass (no
	// literal ".." to Split). Post-fix, extractWildcardKey decodes
	// to "../etc/passwd" and ValidateStorageKey catches it.
	cases := []string{
		"%2E%2E%2Fetc%2Fpasswd",       // ../etc/passwd
		"avatars%2F%2E%2E%2Fsecrets",  // avatars/../secrets
	}
	for _, encoded := range cases {
		t.Run(encoded, func(t *testing.T) {
			var extracted string
			r := chi.NewRouter()
			r.Route("/storage", func(r chi.Router) {
				r.Delete("/*", func(w http.ResponseWriter, req *http.Request) {
					extracted = extractWildcardKey(req)
					w.WriteHeader(http.StatusOK)
				})
			})
			req := httptest.NewRequest(http.MethodDelete, "/storage/"+encoded, nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if !strings.Contains(extracted, "..") {
				t.Fatalf("expected decoded key to contain literal '..'; got %q — decode-first ordering broken", extracted)
			}
			if err := ValidateStorageKey(extracted); err == nil {
				t.Errorf("ValidateStorageKey accepted decoded traversal key %q (must reject '..' segment)", extracted)
			}
		})
	}
}
