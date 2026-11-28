package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// Every legal_documents row a migration inserts with a real checksum
// must match the SHA-256 of the file it names, so a wording change after
// the migration was written can't be published under the old checksum.
func TestLegalDocumentChecksumsMatchSources(t *testing.T) {
	root := filepath.Join("..", "..")
	files, err := filepath.Glob(filepath.Join(root, "migrations", "*.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	row := regexp.MustCompile(`\('(\w+)',\s*'([\d.]+)',\s*'([0-9a-f]{64})',[^)]*'(docs/legal/[^']+)'\)`)
	seen := 0
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range row.FindAllStringSubmatch(string(b), -1) {
			src, err := os.ReadFile(filepath.Join(root, m[4]))
			if err != nil {
				t.Fatalf("%s: %s v%s: %v", filepath.Base(f), m[1], m[2], err)
			}
			sum := sha256.Sum256(src)
			if got := hex.EncodeToString(sum[:]); got != m[3] {
				t.Errorf("%s: %s v%s checksum %s, but %s hashes to %s — recompute it in the migration",
					filepath.Base(f), m[1], m[2], m[3], m[4], got)
			}
			seen++
		}
	}
	if seen == 0 {
		t.Fatal("no legal_documents rows with a checksum found; did the migration format change?")
	}
}
