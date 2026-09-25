package query

import (
	"fmt"
	"strings"
)

// ValidateNoRoutineOrSessionObjects is the extra check for customer SQL
// that runs unattended as a tenant's function role (edge-function SQL,
// cron). It mirrors the runner's guard (functions-runner/sql_guard.ts)
// and refuses:
//   - DO blocks and CREATE [OR REPLACE] FUNCTION / PROCEDURE: bodies are
//     opaque to the lexical checks, and these paths never need them
//     (create routines through migrations or the console);
//   - temporary objects (CREATE TEMP / TEMPORARY …, SELECT … INTO TEMP,
//     pg_temp references) and set_config(): session-scoped state that
//     these single-statement jobs have no use for.
//
// Like the other lexical checks this is a backstop; the boundary is the
// tenant role the SQL runs as.
func ValidateNoRoutineOrSessionObjects(sql string) error {
	stmts, err := splitPrivStatements(sql)
	if err != nil {
		return err
	}
	for _, st := range stmts {
		kw := func(i int) string {
			if i >= 0 && i < len(st) {
				return st[i].kw
			}
			return ""
		}
		switch kw(0) {
		case "do":
			return routineErr("DO blocks")
		case "create":
			j := 1
			if kw(j) == "or" && kw(j+1) == "replace" {
				j += 2
			}
			if kw(j) == "function" || kw(j) == "procedure" {
				return routineErr("CREATE " + strings.ToUpper(kw(j)))
			}
		}
		for i, w := range st {
			if w.name == "pg_temp" || w.name == "set_config" {
				return routineErr(w.name)
			}
			if w.kw == "temp" || w.kw == "temporary" {
				switch kw(i - 1) {
				case "create", "global", "local", "into":
					return routineErr("temporary objects")
				}
			}
		}
	}
	return nil
}

func routineErr(what string) error {
	return fmt.Errorf("%s not allowed here", what)
}
