package query

import (
	"fmt"
	"strings"
)

// CrossSchemaOptions tunes the strict cross-schema check for callers
// whose privilege context allows a small allowlist of qualified
// `public.<name>` references.
//
// AllowedPublicNames is a set of lowercase identifiers that, when they
// appear immediately after `public.`, are treated as legitimate and
// skipped by the forbidden-schema scan. Used by the platform SQL path
// (which runs as migrator and has EXECUTE on RLS helpers like
// `public.is_service_role()` and id-default helpers like
// `public.uuid_generate_v4()`).
//
// The check is textual, not typed — an entry in AllowedPublicNames
// exempts any `public.<name>` reference regardless of whether the
// reference is actually a function call, a type cast, or a table read.
// Names should therefore be chosen from Eurobase-controlled identifiers
// that are not also table names. `public.subscriptions` and other
// billing/tenant tables stay rejected because their names are not in
// this list.
type CrossSchemaOptions struct {
	AllowedPublicNames map[string]bool
}

// ValidateNoCrossSchemaRefs returns an error if the SQL contains a
// qualified reference (`schema.relation`) where the schema is not the
// caller's tenant schema and is not `pg_temp`. Zero-exemption variant —
// use ValidateNoCrossSchemaRefsOpts to allowlist specific
// `public.<name>` references.
//
// Closes advisory GHSA-5cj5-c9f7-9gcj — without this check the gateway
// pool's broad cross-schema grants combined with the service-role RLS
// bypass let any caller with a secret API key read another tenant's
// data via `SELECT * FROM tenant_<other-uuid>.users`.
//
// The check uses a state-machine scanner that recognises:
//   - single-quoted strings (`'…'`, with `''` escape)
//   - double-quoted identifiers (`"…"`, with `""` escape)
//   - dollar-quoted strings (`$$…$$` and `$tag$…$tag$`)
//   - line comments (`-- …`)
//   - block comments (`/* … */`, may nest)
//
// All five are skipped — content inside them isn't treated as code.
//
// Tokens outside those constructs are collected. The scanner reports any
// `identifier . identifier` pair where the leading identifier names a
// forbidden schema. This is a textual check rather than an AST parse,
// so it's deliberately strict: the only legitimate qualified references
// for an SDK caller's tenant are to its own schema, so disallowing
// everything else has near-zero legitimate impact.
//
// False positives can occur if an alias or column name matches a
// forbidden schema (e.g. `SELECT public.col FROM t public`). The fix for
// such queries is to alias to a non-schema name (`SELECT t.col FROM t`).
//
// Defence-in-depth alongside `SET LOCAL search_path TO {tenant}` in the
// engine, the per-tenant role split planned for a follow-up PR, and the
// existing `is_service_role()` policy bypass for secret-key callers.
func ValidateNoCrossSchemaRefs(sql, allowedSchema string) error {
	return ValidateNoCrossSchemaRefsOpts(sql, allowedSchema, CrossSchemaOptions{})
}

// ValidateNoCrossSchemaRefsOpts is the exemption-aware variant. See
// ValidateNoCrossSchemaRefs for the base behaviour and CrossSchemaOptions
// for the allowlist semantics.
func ValidateNoCrossSchemaRefsOpts(sql, allowedSchema string, opts CrossSchemaOptions) error {
	allowed := strings.ToLower(allowedSchema)
	idents := scanIdentifiersAndDots(sql)

	// Walk the token stream. When we see ident, dot, ident in sequence,
	// the leading ident is in the schema position.
	for i := 0; i+2 < len(idents); i++ {
		if idents[i].kind != tokIdent {
			continue
		}
		if idents[i+1].kind != tokDot {
			continue
		}
		if idents[i+2].kind != tokIdent {
			continue
		}
		schema := strings.ToLower(idents[i].value)
		if schema == allowed || schema == "pg_temp" {
			continue
		}
		if schema == "public" && opts.AllowedPublicNames != nil {
			trailing := strings.ToLower(idents[i+2].value)
			if opts.AllowedPublicNames[trailing] {
				continue
			}
		}
		if schemaIsForbidden(schema) {
			return fmt.Errorf("references to schema %q are not allowed (only the caller's tenant schema is in scope)", idents[i].value)
		}
	}
	return nil
}

// PlatformSQLPublicAllowlist is the set of `public.<name>` references
// permitted on the platform SQL endpoint (POST /platform/.../data/sql
// and /data/sql/transaction). Kept as a single source of truth so the
// endpoint's exemption stays in sync with what the console UI and docs
// actively generate.
//
// Contents:
//   - is_service_role, current_end_user_id, is_internal_auth_path — RLS
//     policy helpers the console's docs page (rls-authoring) tells users
//     to reference qualified as `public.is_service_role() OR
//     public.current_end_user_id() = user_id`.
//   - uuid_generate_v4, gen_random_uuid — id-default helpers the console
//     Table Editor generates as `public.uuid_generate_v4()` for new
//     table primary keys.
//
// Deliberately NOT included: names that could also be Eurobase-owned
// tables (subscriptions, invoices, projects, platform_users, webhooks).
// Those stay blocked so the exemption can't be used to smuggle a
// cross-tenant table read past the check.
//
// Qualified references to `public` extension **types** (e.g.
// `col::public.citext`, `col::public.vector`) are also NOT exempted
// and stay rejected — the check is textual, so any exemption would
// have to be by trailing name, which reopens the table hole (there's
// no textual way to tell `public.citext` from `public.subscriptions`).
// Workaround for users: drop the `public.` qualifier and let it
// resolve via search_path (`::citext` works because public is in the
// path).
//
// Migrations path (ValidateTenantMigrationSQL in tenant_migrations.go)
// has its own overlapping list — it excludes uuid_generate_v4 because
// the per-tenant _ddl role lacks EXECUTE on it; the platform path runs
// as migrator which does have EXECUTE. Do not simply merge the two
// lists.
var PlatformSQLPublicAllowlist = map[string]bool{
	"is_service_role":       true,
	"current_end_user_id":   true,
	"is_internal_auth_path": true,
	"uuid_generate_v4":      true,
	"gen_random_uuid":       true,
}

// schemaIsForbidden returns true for schema names that an SDK caller has
// no legitimate reason to reach. The list is conservative: anything not
// the caller's own schema or pg_temp is suspect, but we explicitly reject
// the high-value targets so the error message is precise.
func schemaIsForbidden(s string) bool {
	switch s {
	case "public", "pg_catalog", "information_schema":
		return true
	}
	if strings.HasPrefix(s, "tenant_") {
		return true
	}
	if strings.HasPrefix(s, "pg_") && s != "pg_temp" {
		// pg_toast, pg_global, pg_internal — never legitimately
		// referenced from user SQL.
		return true
	}
	return false
}

type tokKind int

const (
	tokIdent tokKind = iota
	tokDot
)

type token struct {
	kind  tokKind
	value string
}

// scanIdentifiersAndDots produces a token stream of identifiers and dots
// from sql, skipping string/comment/dollar-quoted regions. Whitespace and
// other punctuation are not emitted; consecutive idents (no dot between)
// stay as separate tokens.
func scanIdentifiersAndDots(sql string) []token {
	var out []token
	s := sql
	i := 0
	n := len(s)
	for i < n {
		c := s[i]
		switch {
		case c == '\'':
			// Single-quoted string.
			i++
			for i < n {
				if s[i] == '\'' {
					if i+1 < n && s[i+1] == '\'' {
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
		case c == '"':
			// Double-quoted identifier — emit as ident.
			i++
			var b strings.Builder
			for i < n {
				if s[i] == '"' {
					if i+1 < n && s[i+1] == '"' {
						b.WriteByte('"')
						i += 2
						continue
					}
					i++
					break
				}
				b.WriteByte(s[i])
				i++
			}
			out = append(out, token{kind: tokIdent, value: b.String()})
		case c == '-' && i+1 < n && s[i+1] == '-':
			// Line comment.
			i += 2
			for i < n && s[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && s[i+1] == '*':
			// Block comment, may nest.
			i += 2
			depth := 1
			for i < n && depth > 0 {
				if i+1 < n && s[i] == '/' && s[i+1] == '*' {
					depth++
					i += 2
					continue
				}
				if i+1 < n && s[i] == '*' && s[i+1] == '/' {
					depth--
					i += 2
					continue
				}
				i++
			}
		case c == '$':
			// Dollar-quoted string with optional tag.
			tagEnd := i + 1
			for tagEnd < n && isCrossSchemaDollarTagChar(s[tagEnd], tagEnd == i+1) {
				tagEnd++
			}
			if tagEnd >= n || s[tagEnd] != '$' {
				// Stray $.
				i++
				continue
			}
			tag := s[i : tagEnd+1]
			closer := strings.Index(s[tagEnd+1:], tag)
			if closer < 0 {
				return out
			}
			i = tagEnd + 1 + closer + len(tag)
		case c == '.':
			out = append(out, token{kind: tokDot})
			i++
		case isIdentStart(c):
			start := i
			i++
			for i < n && isIdentCont(s[i]) {
				i++
			}
			out = append(out, token{kind: tokIdent, value: s[start:i]})
		default:
			i++
		}
	}
	return out
}

func isCrossSchemaDollarTagChar(c byte, first bool) bool {
	if first {
		return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
	}
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentCont(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}
