package query

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// Closes #52. SDK callers must not see Detail/Hint/internal paths from
// pgx errors. These tests cover the canonical mappings + the
// fall-through path for unrecognised errors.

func pgErr(code, message, detail, hint string) error {
	// Wrap in fmt.Errorf the same way the engine does so the test
	// exercises the strip-prefix logic too.
	return fmt.Errorf("execute query: %w", &pgconn.PgError{
		Code:    code,
		Message: message,
		Detail:  detail,
		Hint:    hint,
	})
}

func TestSanitizeSDKSQLError_UniqueViolation(t *testing.T) {
	got := sanitizeSDKSQLError(pgErr("23505",
		`duplicate key value violates unique constraint "users_email_key"`,
		"Key (email)=(victim@example.com) already exists.",
		"",
	))
	want := "value violates a unique constraint"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if strings.Contains(got, "victim@example.com") {
		t.Error("Detail leaked into sanitised message")
	}
}

func TestSanitizeSDKSQLError_ForeignKeyViolation(t *testing.T) {
	got := sanitizeSDKSQLError(pgErr("23503", `insert or update on table "posts" violates foreign key constraint "posts_user_id_fkey"`, `Key (user_id)=(0123) is not present in table "users".`, ""))
	if got != "foreign key violation" {
		t.Errorf("got %q", got)
	}
}

func TestSanitizeSDKSQLError_NotNullViolation(t *testing.T) {
	got := sanitizeSDKSQLError(pgErr("23502", "null value in column \"title\" of relation \"posts\" violates not-null constraint", "", ""))
	if got != "value violates a not-null constraint" {
		t.Errorf("got %q", got)
	}
}

func TestSanitizeSDKSQLError_TypeError(t *testing.T) {
	got := sanitizeSDKSQLError(pgErr("22P02", "invalid input syntax for type integer: \"abc\"", "", ""))
	if got != "invalid value for column type" {
		t.Errorf("got %q", got)
	}
}

func TestSanitizeSDKSQLError_PermissionDenied(t *testing.T) {
	got := sanitizeSDKSQLError(pgErr("42501", "permission denied for table tenant_other.users", "", ""))
	if got != "permission denied" {
		t.Errorf("got %q", got)
	}
	if strings.Contains(got, "tenant_other") {
		t.Error("schema name leaked")
	}
}

func TestSanitizeSDKSQLError_BadColumn_KeepsMessageOnly(t *testing.T) {
	got := sanitizeSDKSQLError(pgErr("42703",
		`column "foo" does not exist`,
		"",
		"Perhaps you meant to reference the column \"users.foo_id\".",
	))
	// 42xxx (other than 42501) returns the Message — Hint must NOT leak.
	if got != `column "foo" does not exist` {
		t.Errorf("got %q", got)
	}
	if strings.Contains(got, "users.foo_id") {
		t.Error("Hint leaked")
	}
}

func TestSanitizeSDKSQLError_QueryTimeout(t *testing.T) {
	got := sanitizeSDKSQLError(pgErr("57014", "canceling statement due to statement timeout", "", ""))
	if got != "query timed out" {
		t.Errorf("got %q", got)
	}
}

func TestSanitizeSDKSQLError_UnknownPgError_FallsBackToMessageOnly(t *testing.T) {
	got := sanitizeSDKSQLError(pgErr("99999", "some unrecognised pg error", "internal detail with secrets", "hint with paths"))
	if got != "some unrecognised pg error" {
		t.Errorf("got %q", got)
	}
	if strings.Contains(got, "secrets") || strings.Contains(got, "paths") {
		t.Error("Detail or Hint leaked on fallback")
	}
}

func TestSanitizeSDKSQLError_NonPgError_GenericFallback(t *testing.T) {
	got := sanitizeSDKSQLError(fmt.Errorf("execute query: %w", errors.New("connection refused")))
	if got != "query execution failed" {
		t.Errorf("got %q", got)
	}
}

func TestSanitizeSDKSQLError_OwnValidatorErrors_PassThrough(t *testing.T) {
	for _, msg := range []string{
		"only SELECT statements are allowed via /v1/db/sql",
		"forbidden cross-schema reference: tenant_other.users",
		"input contains multiple SQL statements; this endpoint accepts a single statement.",
	} {
		got := sanitizeSDKSQLError(errors.New(msg))
		if got != msg {
			t.Errorf("validator message changed: got %q, want %q", got, msg)
		}
	}
}

func TestSanitizeSDKSQLError_StripsExecuteQueryPrefix(t *testing.T) {
	// Engine wraps everything as fmt.Errorf("execute query: %w", err).
	// Caller-safe validator messages should still surface without the
	// internal stage name.
	got := sanitizeSDKSQLError(fmt.Errorf("execute query: %w", errors.New("only SELECT statements are allowed")))
	if !strings.HasPrefix(got, "only SELECT") {
		t.Errorf("expected stripped prefix, got %q", got)
	}
	if strings.HasPrefix(got, "execute query") {
		t.Errorf("internal stage name leaked: %q", got)
	}
}
