package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The newest legal_documents row a migration inserts for each source file
// must carry the SHA-256 of that file as it is now, so a wording change
// after the migration was written can't be published under the old
// checksum. Older rows describe earlier texts and aren't checked (their
// files have changed since, by design). A row the pattern can't read
// fails the test instead of being skipped.
func TestLegalDocumentChecksumsMatchSources(t *testing.T) {
	root := filepath.Join("..", "..")
	files, err := filepath.Glob(filepath.Join(root, "migrations", "*.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files) // migration order
	row := regexp.MustCompile(`^\s*\('(\w+)',\s*'([\d.]+)',\s*'([0-9a-f]{64})',\s*'[^']*',\s*'(docs/legal/[^']+)'\)`)
	type entry struct{ migration, doc, version, sum string }
	newest := map[string]entry{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "legal_documents") {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			// Value rows only (comments and column definitions mention
			// the paths too); rows still 'PENDING' have no checksum.
			if !strings.HasPrefix(strings.TrimSpace(line), "('") || !strings.Contains(line, "'docs/legal/") || strings.Contains(line, "'PENDING'") {
				continue
			}
			m := row.FindStringSubmatch(line)
			if m == nil {
				t.Errorf("%s: can't read this legal_documents row; use ('type', 'version', '<sha256>', '<effective_at>', 'docs/legal/…'):\n%s", filepath.Base(f), line)
				continue
			}
			newest[m[4]] = entry{filepath.Base(f), m[1], m[2], m[3]}
		}
	}
	if len(newest) == 0 {
		t.Fatal("no legal_documents rows with a checksum found")
	}
	for path, e := range newest {
		src, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatalf("%s: %s v%s: %v", e.migration, e.doc, e.version, err)
		}
		sum := sha256.Sum256(src)
		if got := hex.EncodeToString(sum[:]); got != e.sum {
			t.Errorf("%s: %s v%s has checksum %s, but %s now hashes to %s — recompute it in that migration (if not yet applied) or add a new version",
				e.migration, e.doc, e.version, e.sum, path, got)
		}
	}
}
