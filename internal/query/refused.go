package query

import (
	"errors"
	"fmt"

	"github.com/eurobase/euroback/internal/sqllog"
)

// RefusedError marks an error from the platform's own checks on SQL a
// developer wrote — returned before anything ran — as opposed to an error
// from the database. The SQL log records it as "refused". The message is
// the wrapped error's, unchanged.
type RefusedError struct{ Err error }

func (e *RefusedError) Error() string { return e.Err.Error() }
func (e *RefusedError) Unwrap() error { return e.Err }

// IsRefused reports whether err (or an error it wraps) is a RefusedError.
func IsRefused(err error) bool {
	var r *RefusedError
	return errors.As(err, &r)
}

func refusedf(format string, a ...any) error {
	return &RefusedError{Err: fmt.Errorf(format, a...)}
}

func refused(err error) error {
	if err == nil {
		return nil
	}
	return &RefusedError{Err: err}
}

// sqlLogOutcome is the SQL log outcome for a failed statement: refused by
// the platform's checks, or an error from the database.
func sqlLogOutcome(err error) string {
	if IsRefused(err) {
		return sqllog.OutcomeRefused
	}
	return sqllog.OutcomeError
}
