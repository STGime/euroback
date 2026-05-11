package query

import (
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// sanitizeSDKSQLError trims a pgx error down to a minimal, safe message
// for SDK callers (eb_pk_/eb_sk_ traffic on /v1/db/sql). Closes #52.
//
// The unsanitized pgx Error() output can include Detail (which
// frequently carries the offending column value), Hint, internal file
// paths, and full query position info. None of that should reach a
// browser/SDK caller — it's an information-leak vector. Platform
// callers (console + MCP, /platform/.../data/sql) keep the full
// message because the platform admin running ad-hoc SQL needs the
// detail; server-side slog.Error logs both forms either way.
//
// For known SQLSTATE classes we emit a fixed, readable message keyed
// to the class. For everything else we fall back to the PgError's
// Message field only (no Detail, no Hint), or a generic message if
// the error wasn't a PgError at all.
func sanitizeSDKSQLError(err error) string {
	if err == nil {
		return ""
	}

	// Engine wraps everything as fmt.Errorf("execute query: %w", err).
	// Strip the "execute query: " prefix so the SDK caller doesn't see
	// internal stage names.
	msg := err.Error()
	msg = strings.TrimPrefix(msg, "execute query: ")

	// Validation errors generated in this package (cross-schema guard,
	// SELECT-only check, multi-statement guard) are already caller-safe.
	// Pass them through unchanged.
	if !isPostgresError(err) {
		// Non-pg errors that aren't recognised: refuse to leak.
		// Most validation errors do reach this path; whitelist them by
		// substring rather than by type to keep the change minimal.
		if isCallerSafeMessage(msg) {
			return msg
		}
		return "query execution failed"
	}

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return "query execution failed"
	}

	switch sqlStateClass(pgErr.Code) {
	case "23": // integrity constraint violation
		switch pgErr.Code {
		case "23505":
			return "value violates a unique constraint"
		case "23503":
			return "foreign key violation"
		case "23502":
			return "value violates a not-null constraint"
		case "23514":
			return "value violates a check constraint"
		}
		return "constraint violation"
	case "22": // data exception
		return "invalid value for column type"
	case "42": // syntax error or access rule violation
		if pgErr.Code == "42501" {
			return "permission denied"
		}
		// Bad column / table / SQL syntax — Message is safe (just the
		// identifier name), Detail / Hint / Position are not.
		return pgErr.Message
	case "40": // transaction rollback
		return "transaction was rolled back"
	case "57": // operator intervention (admin shutdown, query cancelled, etc.)
		if pgErr.Code == "57014" {
			return "query timed out"
		}
		return "query cancelled"
	case "53": // resource limit
		return "resource limit exceeded"
	}

	// Anything we don't classify: fall back to Message only.
	if pgErr.Message != "" {
		return pgErr.Message
	}
	return "query execution failed"
}

// isPostgresError reports whether err (or its wrapped chain) is a pgx
// PgError. Used to decide between "this came from postgres so go via
// the SQLSTATE map" and "this is one of our own validators, pass through".
func isPostgresError(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr)
}

// sqlStateClass returns the two-character class portion of an SQLSTATE
// code, e.g. "23" for "23505". Empty if the code is malformed.
func sqlStateClass(code string) string {
	if len(code) >= 2 {
		return code[:2]
	}
	return ""
}

// isCallerSafeMessage allow-lists messages produced by this package's
// own validators. Adding a new validator? Either include a recognisable
// prefix here or wrap the error so it doesn't leak.
func isCallerSafeMessage(msg string) bool {
	// All of these come from validate.go / sql_validate.go / multistmt.go
	// and contain only static text + the offending fragment.
	for _, prefix := range []string{
		"only SELECT",
		"forbidden cross-schema reference",
		"input contains multiple SQL statements",
		"unsupported aggregate",
		"invalid column",
		"invalid table",
		"invalid identifier",
		"a required field",
		"limit ",
	} {
		if strings.HasPrefix(msg, prefix) {
			return true
		}
	}
	return false
}
