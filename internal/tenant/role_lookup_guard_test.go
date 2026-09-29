package tenant

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every access decision goes through CallerProjectRole (and
// OwnerOrOrgAdmin for deletes), so a session limit added there — e.g. a
// project-scoped token's role (#702) — applies everywhere. A new direct
// lookup elsewhere would decide access around it.
//
// Known exceptions, outside what this scan sees (raw SQL on owner_id /
// project_members): SetProjectOrg (behind RequireRole "owner"),
// support_handlers.userIsProjectMember, billing's owner checks, the
// dev-only TenantContextMiddleware. Project-scoped tokens never reach them
// (auth.ScopedTokenAllowed refuses those routes).
func TestNoDirectRoleLookups(t *testing.T) {
	// Any reference — a call, a method value, across a comment — not only
	// "Name(" on one line.
	re := regexp.MustCompile(`\b(IsProjectAccessible|ResolveRole)\b`)
	allowed := map[string]bool{
		filepath.Join("tenant", "access.go"): true, // CallerProjectRole, OwnerOrOrgAdmin, IsProjectAccessible
	}
	root := ".."
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if allowed[rel] {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(b), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			if i := strings.Index(line, "//"); i >= 0 {
				line = line[:i] // a trailing comment isn't code
			}
			// ResolveRole's own definition lives in members.go.
			if rel == filepath.Join("tenant", "members.go") && strings.HasPrefix(trimmed, "func ResolveRole(") {
				continue
			}
			if re.MatchString(line) {
				t.Errorf("%s:%d: direct role lookup — use tenant.CallerProjectRole: %s", rel, i+1, trimmed)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
