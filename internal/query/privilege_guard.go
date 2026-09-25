package query

import (
	"fmt"
	"strings"
)

// ValidateNoPrivilegeStatements rejects SQL that changes roles, session
// identity, search_path, privileges or ownership. These statement shapes
// are never needed to manage a tenant's own tables, and each can change
// who the rest of a transaction runs as or what other roles may access.
//
// This is a lexical backstop, not the primary control: it cannot see
// inside function bodies, DO blocks or dynamically built SQL (the same
// residual documented for ValidateNoCatalogRefs). The real boundary is
// the database role the SQL runs as.
//
// Uses the comment/string-aware tokenizer shared with the cross-schema
// check. Quoted identifiers are names, never keywords — but they are
// still compared (case-folded) against the configuration-parameter names
// below, because `SET "role"` / `"set_config"(…)` resolve the same way.
func ValidateNoPrivilegeStatements(sql string) error {
	stmts, err := splitPrivStatements(sql)
	if err != nil {
		return err
	}
	for _, st := range stmts {
		if err := checkPrivilegeStatement(st); err != nil {
			return err
		}
	}
	return nil
}

// splitPrivStatements tokenizes sql (comment/string/dollar-quote aware)
// into statements of privWords.
func splitPrivStatements(sql string) ([][]privWord, error) {
	var stmts [][]privWord
	cur := []privWord{}
	for _, t := range scanIdentifiersAndDots(sql) {
		switch t.kind {
		case tokSemi:
			stmts = append(stmts, cur)
			cur = []privWord{}
		case tokDot:
			cur = append(cur, privWord{})
		default:
			if t.unicodeEscaped {
				return nil, privErr(`U&"…" identifiers`)
			}
			w := privWord{name: strings.ToLower(t.value)}
			if !t.quoted {
				w.kw = w.name
			}
			cur = append(cur, w)
		}
	}
	return append(stmts, cur), nil
}

// privWord is one token of a statement for the privilege guard.
type privWord struct {
	kw   string // lower-cased keyword; "" for quoted identifiers and dots
	name string // lower-cased value for identifiers (quoted or not)
}

func checkPrivilegeStatement(st []privWord) error {
	kw := func(i int) string {
		if i >= 0 && i < len(st) {
			return st[i].kw
		}
		return ""
	}
	first := kw(0)

	// Transaction control: the platform scopes each request inside its own
	// transaction, so customer SQL may not end, nest or split it (same
	// rule as the tenant-migrations validator). Statement-start only, so
	// CASE … END is unaffected.
	switch first {
	case "begin", "commit", "rollback", "savepoint", "release", "end", "abort", "start", "prepare":
		return privErr(strings.ToUpper(first) + " (transaction control)")
	}

	// SET / RESET as a *command* (statement start). `UPDATE … SET col`
	// never reaches this branch, so role/owner-named columns stay usable.
	if first == "set" || first == "reset" {
		j := 1
		for kw(j) == "local" || kw(j) == "session" {
			j++
		}
		target := ""
		if j < len(st) {
			target = st[j].name
		}
		switch target {
		case "role", "all", "search_path", "session", "authorization", "session_authorization":
			return privErr(strings.ToUpper(first + " " + target))
		}
	}

	for i, w := range st {
		// Configuration-parameter names and the catalog view that
		// writes them: denied wherever they appear, quoted or not.
		// The string-literal settings change how the server tokenizes
		// quotes, so the lexical checks here would read later text
		// differently from the server — never allowed.
		switch w.name {
		case "search_path", "set_config", "session_authorization", "pg_settings",
			"standard_conforming_strings", "backslash_quote", "escape_string_warning":
			return privErr(strings.ToUpper(w.name))
		}
		switch w.kw {
		case "grant", "revoke", "reassign":
			return privErr(strings.ToUpper(w.kw))
		case "authorization":
			// SESSION AUTHORIZATION, CREATE SCHEMA … AUTHORIZATION.
			return privErr("AUTHORIZATION")
		case "privileges":
			if kw(i-1) == "default" {
				return privErr("ALTER DEFAULT PRIVILEGES")
			}
		case "role", "user", "group":
			switch kw(i - 1) {
			case "create", "alter", "drop":
				return privErr(strings.ToUpper(kw(i-1) + " " + w.kw))
			}
		case "owned":
			if kw(i-1) == "drop" {
				return privErr("DROP OWNED")
			}
		case "owner":
			if kw(i+1) == "to" {
				return privErr("OWNER TO")
			}
		case "definer":
			if kw(i-1) == "security" {
				return privErr("SECURITY DEFINER")
			}
		}
	}
	return nil
}

func privErr(what string) error {
	return fmt.Errorf("%s is not allowed here: role, session and privilege management is handled by the platform", what)
}
