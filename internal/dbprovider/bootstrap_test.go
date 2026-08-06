package dbprovider

import (
	"strings"
	"testing"
)

// The bootstrap flow needs a live Postgres to exercise end-to-end
// (CREATE ROLE, provision_tenant, RLS). Those tests live in staging
// E2E — the RLS-isolation assertion is the safety gate for flipping
// TEAM_TIER_ROUTING (see #338).
//
// This file pins the two unit-testable safety properties of
// bootstrap.go:
//   * The runtime password shape is exactly what dedicated_bootstrap.sql
//     assumes (64 lowercase hex chars, no escape characters).
//   * isHexChars refuses anything that could carry a SQL literal
//     out of the ALTER ROLE … PASSWORD statement — the one place we
//     interpolate a secret into DDL because pgx doesn't bind DDL
//     parameters.

func TestRandomHexPassword32_ShapeAndUniqueness(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 32; i++ {
		p, err := randomHexPassword32()
		if err != nil {
			t.Fatalf("randomHexPassword32: %v", err)
		}
		if len(p) != 64 {
			t.Fatalf("password length: got %d want 64", len(p))
		}
		if !isHexChars(p) {
			t.Fatalf("password not hex: %q", p)
		}
		if seen[p] {
			t.Fatalf("duplicate password in 32 draws: %q", p)
		}
		seen[p] = true
	}
}

func TestIsHexChars_AcceptsHex(t *testing.T) {
	for _, s := range []string{
		"deadbeef",
		"0123456789abcdefABCDEF",
		strings.Repeat("f", 64),
	} {
		if !isHexChars(s) {
			t.Errorf("hex %q rejected", s)
		}
	}
}

func TestIsHexChars_RejectsNonHex(t *testing.T) {
	// These are the values a broken future random-password generator
	// might return, or that a caller might pass by mistake. Rejecting
	// them is the last line of defence against SQL-literal injection
	// in the ALTER ROLE … PASSWORD statement, which cannot use pgx
	// bind parameters (Postgres doesn't accept parameters in DDL).
	cases := []string{
		"",              // empty
		"' OR '1'='1",   // classic SQL-injection payload
		"deadbeef'; --", // hex prefix then break
		"pass\nwrd",     // newline
		"pass word",     // space
		"héllo",         // unicode
		"nothex",        // 'g' at end
	}
	for _, s := range cases {
		if isHexChars(s) {
			t.Errorf("non-hex %q accepted", s)
		}
	}
}

// The bootstrap SQL must not embed the runtime password at all
// (it's committed to source; must contain only literal-safe
// statements). Guard against a future edit accidentally landing a
// PASSWORD clause outside a comment.
func TestDedicatedBootstrapSQL_DoesNotEmbedPassword(t *testing.T) {
	// Comments explain the bootstrap flow (which mentions PASSWORD)
	// so we strip -- line comments before searching. Executable
	// statements are what matters.
	executable := stripSQLLineComments(strings.ToLower(dedicatedBootstrapSQL))
	if strings.Contains(executable, "password '") || strings.Contains(executable, "password $") {
		t.Fatal("dedicated_bootstrap.sql executable text embeds a PASSWORD literal — must stay in bootstrap.go")
	}
	// A NOLOGIN role is the correct shape here — a LOGIN role would
	// need a password.
	if !strings.Contains(executable, "nologin") {
		t.Fatal("dedicated_bootstrap.sql should CREATE ROLE eurobase_gateway NOLOGIN — LOGIN is bootstrap.go's job")
	}
}

func stripSQLLineComments(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, line := range strings.Split(s, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// The tenant-schema shape must stay in lockstep with the shared
// cluster's provision_tenant (migration 000063). This test lists the
// six tables that MUST be created — a future edit that drops one
// silently would fail auth / storage / vault in production.
func TestDedicatedBootstrapSQL_CreatesAllTenantTables(t *testing.T) {
	// Case-insensitive; the SQL uses `format('CREATE TABLE %I.users …)`.
	lowered := strings.ToLower(dedicatedBootstrapSQL)
	for _, tbl := range []string{
		"i.users ",
		"i.user_identities ",
		"i.refresh_tokens ",
		"i.email_tokens ",
		"i.storage_objects ",
		"i.vault_secrets ",
		"i.todos ",
	} {
		if !strings.Contains(lowered, "create table %"+tbl) {
			t.Errorf("bootstrap SQL missing table: %q", tbl)
		}
	}
}
