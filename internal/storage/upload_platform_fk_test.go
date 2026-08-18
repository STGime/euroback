package storage

import (
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/eurobase/euroback/internal/auth"
	"github.com/jackc/pgx/v5/pgconn"
)

// Regression coverage for the day-1 console-upload silent-FK-failure
// bug. The `storage_objects.uploaded_by` column FKs to the TENANT
// `users` table (migration 000063). SDK uploads carry an end-user JWT
// whose UserID is a real row there; console uploads carry a
// platform_users.id which isn't. The pre-fix INSERT tried to write
// the platform ID either way and silently FK-violated, leaving every
// console-uploaded file as a "list-only" object that 404'd on
// download/delete/preview. Verified against live: `SELECT count(*)
// FROM storage_objects` in a project with 2 console-uploaded objects
// returned 0.
//
// uploaderForInsert returns nil (SQL NULL) on the platform path so
// the FK check passes; the column is nullable, no schema change.

func TestUploaderForInsert_EndUserPath(t *testing.T) {
	req := httptest.NewRequest("POST", "/upload", nil)
	req = req.WithContext(auth.ContextWithEndUserClaims(req.Context(), &auth.EndUserClaims{
		UserID: "u-abc",
	}))

	got := uploaderForInsert(req)
	if got != "u-abc" {
		t.Errorf("SDK end-user upload: uploaded_by should be the end-user ID, got %v", got)
	}
}

func TestUploaderForInsert_PlatformPath(t *testing.T) {
	req := httptest.NewRequest("POST", "/upload", nil)
	req = req.WithContext(auth.ContextWithClaims(req.Context(), &auth.Claims{
		Subject: "platform-user-id",
		Email:   "dev@example.com",
	}))

	got := uploaderForInsert(req)
	if got != nil {
		t.Errorf("console (platform) upload: uploaded_by must be nil so INSERT stores SQL NULL and passes the FK check (day-1 bug regression guard). Got %v (%T).", got, got)
	}
}

// End-user claims with an empty UserID must not be trusted — treat as
// platform-shaped (return nil) so we don't try to INSERT an empty
// string that pgx would fail to cast to uuid.
func TestUploaderForInsert_EndUserWithEmptyUserID(t *testing.T) {
	req := httptest.NewRequest("POST", "/upload", nil)
	req = req.WithContext(auth.ContextWithEndUserClaims(req.Context(), &auth.EndUserClaims{
		UserID: "",
	}))

	if got := uploaderForInsert(req); got != nil {
		t.Errorf("empty end-user UserID must be treated as no attribution (nil), got %v", got)
	}
}

// Anonymous requests should also return nil. isAuthenticated is
// checked upstream (401), but uploaderForInsert must never panic on
// a bare context.
func TestUploaderForInsert_NoClaims(t *testing.T) {
	req := httptest.NewRequest("POST", "/upload", nil)
	if got := uploaderForInsert(req); got != nil {
		t.Errorf("no claims: expected nil, got %v", got)
	}
}

// End-user claims take precedence over platform claims when both are
// present in the same context. Not a real prod code path today but
// pins the ordering so a future middleware refactor can't silently
// invert it.
func TestUploaderForInsert_EndUserWinsOverPlatform(t *testing.T) {
	req := httptest.NewRequest("POST", "/upload", nil)
	ctx := auth.ContextWithClaims(req.Context(), &auth.Claims{Subject: "platform-user-id"})
	ctx = auth.ContextWithEndUserClaims(ctx, &auth.EndUserClaims{UserID: "eu-42"})
	req = req.WithContext(ctx)

	if got := uploaderForInsert(req); got != "eu-42" {
		t.Errorf("end-user claims should take precedence, got %v", got)
	}
}

// isForeignKeyViolation must gate the retry-with-NULL branch tightly.
// If it fires on the wrong error class (e.g. connection failure) we'd
// retry with NULL when the DB is unreachable — harmless but noisy.
// If it MISSES a real 23503, we lose the belt-and-suspenders retry
// and the caller sees a 500 that a retry-with-NULL would have saved.
func TestIsForeignKeyViolation(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"unrelated error", errors.New("connection reset"), false},
		{"pgx not-found", errors.New("no rows in result set"), false},
		{"23505 unique violation (not FK)", &pgconn.PgError{Code: "23505"}, false},
		{"23503 foreign key violation", &pgconn.PgError{Code: "23503"}, true},
		{"wrapped 23503", wrapErr(&pgconn.PgError{Code: "23503"}), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isForeignKeyViolation(tc.err); got != tc.want {
				t.Errorf("isForeignKeyViolation(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// Wrap via fmt.Errorf %w so we exercise errors.As on the discriminator.
// A hand-rolled wrapper avoids the fmt import for a one-off use.
type wrap struct{ err error }

func (w *wrap) Error() string { return "wrapped: " + w.err.Error() }
func (w *wrap) Unwrap() error { return w.err }

func wrapErr(err error) error { return &wrap{err: err} }
