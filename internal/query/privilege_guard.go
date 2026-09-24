package query

import (
	"fmt"
	"strings"
)

// ValidateNoPrivilegeStatements rejects SQL that changes roles, session
// identity, search_path, privileges or ownership. Customer SQL on the
// platform paths (console SQL editor, MCP, transaction endpoint) runs
// with elevated database privileges, so none of these statement shapes
// may be accepted there: they are never needed to manage a tenant's own
// tables, and each one can change who the rest of the transaction runs
// as or what other roles may access.
//
// Uses the same comment/string-aware tokenizer as the cross-schema
// check, so keywords inside literals, quoted identifiers and comments
// are ignored. Deliberately strict: a false positive costs a customer a
// rewrite; a false negative is a privilege problem.
func ValidateNoPrivilegeStatements(sql string) error {
	toks := scanIdentifiersAndDots(sql)
	words := make([]string, 0, len(toks))
	for _, t := range toks {
		switch {
		case t.kind == tokIdent && t.quoted:
			// A quoted identifier is a name, never a keyword.
			words = append(words, "")
		case t.kind == tokIdent:
			words = append(words, strings.ToLower(t.value))
		default:
			words = append(words, ".")
		}
	}
	at := func(i int) string {
		if i >= 0 && i < len(words) {
			return words[i]
		}
		return ""
	}

	for i, w := range words {
		switch w {
		case "grant", "revoke", "reassign":
			return privErr(strings.ToUpper(w))
		case "set_config", "search_path":
			return privErr(strings.ToUpper(w))
		case "privileges":
			if at(i-1) == "default" {
				return privErr("ALTER DEFAULT PRIVILEGES")
			}
		case "authorization":
			if at(i-1) == "session" {
				return privErr("SESSION AUTHORIZATION")
			}
		case "role", "user", "group":
			switch at(i - 1) {
			case "set", "reset", "local", "session":
				if w == "role" {
					return privErr("SET/RESET ROLE")
				}
			case "create", "alter", "drop":
				return privErr(strings.ToUpper(at(i-1) + " " + w))
			}
		case "all":
			if at(i-1) == "reset" {
				return privErr("RESET ALL")
			}
		case "owned":
			if at(i-1) == "drop" {
				return privErr("DROP OWNED")
			}
		case "owner":
			if at(i+1) == "to" {
				return privErr("OWNER TO")
			}
		case "definer":
			if at(i-1) == "security" {
				return privErr("SECURITY DEFINER")
			}
		}
	}
	return nil
}

func privErr(what string) error {
	return fmt.Errorf("%s is not allowed here: role, session and privilege management is handled by the platform", what)
}
