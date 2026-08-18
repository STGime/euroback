package storage

import "testing"

// NormalizeStorageKey is the single guarded point that both the
// upload path, the extract (read) path, and the offline backfill
// script all call. The invariant it protects is: the byte sequence
// written to storage_objects.key MUST equal what a subsequent
// lookup produces — otherwise `WHERE key = $1` silently misses and
// the file 404s.
//
// PR #427 review 🟢: pin the helper explicitly so a future refactor
// can't hand-copy `norm.NFC.String` back into one call site and
// silently drift the others.
func TestNormalizeStorageKey(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "NFD ä (a + U+0308) composes to NFC ä",
			in:   "Blätter.jpg", // Bla + combining diaeresis + tter.jpg
			want: "Blätter.jpg",        // single codepoint U+00E4
		},
		{
			name: "NFC ä survives (idempotent)",
			in:   "Blätter.jpg",
			want: "Blätter.jpg",
		},
		{
			name: "NFD ö on Björn",
			in:   "avatars/Björn.png",
			want: "avatars/Björn.png",
		},
		{
			name: "ASCII passthrough",
			in:   "hello.txt",
			want: "hello.txt",
		},
		{
			name: "empty string",
			in:   "",
			want: "",
		},
		{
			name: "emoji (already single codepoint — no compose op)",
			in:   "🚀-launch.pdf",
			want: "🚀-launch.pdf",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeStorageKey(tc.in)
			if got != tc.want {
				t.Errorf("NormalizeStorageKey(%q) = %q (bytes %x); want %q (bytes %x)",
					tc.in, got, []byte(got), tc.want, []byte(tc.want))
			}
		})
	}
}

// Idempotency is the load-bearing invariant for the round-trip:
// upload normalizes → INSERT → extract normalizes → SELECT. If the
// second normalization produced different bytes for an already-NFC
// input, the runtime lookup would miss every already-stored row.
func TestNormalizeStorageKey_Idempotent(t *testing.T) {
	for _, s := range []string{
		"Blätter.jpg",
		"café,menu.pdf",
		"avatars/Björn.png",
		"🚀-launch.pdf",
		"hello.txt",
		"", // edge case
	} {
		once := NormalizeStorageKey(s)
		twice := NormalizeStorageKey(once)
		if once != twice {
			t.Errorf("not idempotent for %q: once=%q, twice=%q", s, once, twice)
		}
	}
}
