package storage

import (
	"strings"
	"testing"
)

// Closes #61: storage key normalisation. The S3 client treats keys as
// opaque strings today, but any future code path that joins a key into
// a filesystem path or local URL becomes path-traversal-vulnerable
// without these checks.

func TestValidateStorageKey_Accepts(t *testing.T) {
	for _, k := range []string{
		"file.txt",
		"folder/file.txt",
		"deep/nested/path/with/many/segments.bin",
		"name with spaces.txt",
		"unicode-éàü.txt",
		"a", // single char
		strings.Repeat("a", 1024), // boundary
	} {
		if err := ValidateStorageKey(k); err != nil {
			t.Errorf("ValidateStorageKey(%q) = %v, want nil", k, err)
		}
	}
}

func TestValidateStorageKey_Rejects(t *testing.T) {
	cases := []struct {
		name string
		key  string
	}{
		{"empty", ""},
		{"too long", strings.Repeat("a", 1025)},
		{"leading slash", "/foo.txt"},
		{"leading slash with subpath", "/folder/file.txt"},
		{"dotdot segment standalone", ".."},
		{"dotdot segment in middle", "folder/../etc/passwd"},
		{"dotdot segment at end", "folder/sub/.."},
		{"NUL byte", "file\x00.txt"},
		{"newline", "file\n.txt"},
		{"carriage return", "file\r.txt"},
		{"tab", "file\t.txt"},
		{"DEL char", "file\x7f.txt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateStorageKey(tc.key); err == nil {
				t.Errorf("ValidateStorageKey(%q) = nil, want error", tc.key)
			}
		})
	}
}

func TestValidateStorageKey_AllowsDotDotInSegment(t *testing.T) {
	// `..` is rejected only as a path SEGMENT. A filename like
	// `archive..tar.gz` or `back..up` should pass.
	for _, k := range []string{"archive..tar.gz", "back..up", "..hidden.txt"} {
		if err := ValidateStorageKey(k); err != nil {
			t.Errorf("ValidateStorageKey(%q) = %v, want nil — `..` only forbidden as a full segment", k, err)
		}
	}
}
