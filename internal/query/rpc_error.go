package query

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// ErrRPCFunctionNotFound: the called function doesn't exist in the
// project's schema.
var ErrRPCFunctionNotFound = errors.New("function not found")

// errRPCInvalidName: the function name has characters other than
// letters, digits and underscores.
var errRPCInvalidName = errors.New("invalid function name")

// rpcError is what an SDK caller sees when POST /v1/db/rpc/{fn} fails.
type rpcError struct {
	Status  int
	Message string
	// Code is the Postgres SQLSTATE, when the error came from the
	// database ("P0001" for RAISE EXCEPTION, "42501" for permission
	// denied, …). Empty otherwise.
	Code string
}

// rpcErrorFor maps an RPC failure to a status and a message safe for an
// SDK caller. Database errors go through the same sanitiser as
// /v1/db/sql (no Detail, Hint or position), so a function's own
// RAISE EXCEPTION message reaches the developer, while values that
// Postgres puts in Detail don't.
//
// The project's schema name never appears in a message: the call is
// schema-qualified, so Postgres names it in e.g. "function
// tenant_x.f(wrong => integer) does not exist"; it is stripped.
func rpcErrorFor(err error, funcName, schema string) rpcError {
	e := rpcErrorForUnscrubbed(err, funcName)
	e.Message = stripSchema(e.Message, schema)
	return e
}

// stripSchema removes "<schema>." and "\"<schema>\"." from msg.
func stripSchema(msg, schema string) string {
	if schema == "" {
		return msg
	}
	msg = strings.ReplaceAll(msg, `"`+schema+`".`, "")
	msg = strings.ReplaceAll(msg, schema+".", "")
	return msg
}

func rpcErrorForUnscrubbed(err error, funcName string) rpcError {
	switch {
	case errors.Is(err, ErrTenantLoginUnavailable):
		return rpcError{Status: http.StatusServiceUnavailable, Message: "the project's database is not available right now"}
	case errors.Is(err, ErrRPCFunctionNotFound):
		return rpcError{Status: http.StatusNotFound, Message: "function " + quoteForMessage(funcName) + " not found"}
	case errors.Is(err, errRPCInvalidName):
		return rpcError{Status: http.StatusBadRequest, Message: "invalid function name"}
	}

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return rpcError{Status: http.StatusInternalServerError, Message: "internal server error"}
	}

	e := rpcError{Message: sanitizeSDKSQLError(err), Code: pgErr.Code}
	switch sqlStateClass(pgErr.Code) {
	case "P0": // raised by the function itself (RAISE EXCEPTION, ASSERT)
		e.Status = http.StatusBadRequest
		if pgErr.Message != "" {
			e.Message = pgErr.Message
		}
	case "42":
		if pgErr.Code == "42501" {
			e.Status = http.StatusForbidden // permission denied / RLS
		} else {
			e.Status = http.StatusBadRequest // wrong arguments, unknown column, …
		}
	case "22", "23":
		e.Status = http.StatusBadRequest
	case "40":
		e.Status = http.StatusConflict
	case "53", "57":
		e.Status = http.StatusServiceUnavailable
	case "08", "58", "F0", "XX": // connection, system, config, internal
		e.Status = http.StatusInternalServerError
		e.Message = "internal server error"
	default: // e.g. 2F function ended without RETURN, 21 cardinality, 0A not supported
		e.Status = http.StatusBadRequest
	}
	return e
}

func quoteForMessage(s string) string {
	return "\"" + s + "\""
}
