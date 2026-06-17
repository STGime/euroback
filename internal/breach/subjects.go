package breach

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/eurobase/euroback/internal/compliance"
	edb "github.com/eurobase/euroback/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SubjectQuery scopes which end-users are considered "affected" by a breach.
// All fields are optional and combined with AND. The default (all-nil) returns
// every user in the tenant's users table — the worst case but sometimes the
// only honest answer. Use Limit to cap the affected_ids list returned to
// callers; the count itself is always exact.
type SubjectQuery struct {
	// CreatedFrom/CreatedUntil bound the users.created_at window. Useful
	// when the breach exposed a sign-up form or a specific batch import.
	CreatedFrom  *time.Time
	CreatedUntil *time.Time
	// UpdatedFrom/UpdatedUntil bound the users.updated_at window. Useful
	// when the breach was a leak of "active sessions" or "last-edited
	// profiles".
	UpdatedFrom  *time.Time
	UpdatedUntil *time.Time
	// UserIDs restricts to a known list of subjects (e.g. from an audit
	// log triage). Returns count = intersection-with-tenant.
	UserIDs []string
	// Limit caps the size of AffectedIDs in the result. 0 = no IDs
	// returned; -1 = unbounded. Default 100 in the handler.
	Limit int
}

// SubjectResult is what IdentifySubjects returns. Count is the size of the
// affected set. AffectedIDs is a sample (up to Limit) — the DPO uses it to
// spot-check the criteria before pulling the full list for notification.
type SubjectResult struct {
	Count        int64    `json:"count"`
	TablesProbed int      `json:"tables_probed"`
	AffectedIDs  []string `json:"affected_ids"`
	SchemaName   string   `json:"schema_name"`
}

// IdentifySubjects counts (and optionally enumerates) the end-users whose
// records fall under a breach's scope. Reuses DiscoverUserTables /
// ListTenantTables from compliance/export.go so we don't drift from the
// DSAR table inventory.
//
// Connects through edb.RunAsService so app.end_user_role='service' is set
// for the transaction; tenant users RLS would otherwise filter every row
// out — there's no end-user actor here, the caller is the DPO.
func IdentifySubjects(ctx context.Context, pool *pgxpool.Pool, projectID string, q SubjectQuery) (*SubjectResult, error) {
	var schemaName string
	if err := pool.QueryRow(ctx,
		`SELECT schema_name FROM projects WHERE id = $1`, projectID,
	).Scan(&schemaName); err != nil {
		return nil, fmt.Errorf("resolve project schema: %w", err)
	}

	whereSQL, args := buildUserWhere(q)
	usersQ := fmt.Sprintf(`SELECT id::text FROM %s.users%s`, quoteIdent(schemaName), whereSQL)
	countQ := fmt.Sprintf(`SELECT COUNT(*) FROM %s.users%s`, quoteIdent(schemaName), whereSQL)

	limit := q.Limit
	if limit == 0 {
		limit = 100
	}
	if limit > 0 {
		usersQ += fmt.Sprintf(" LIMIT %d", limit)
	}

	result := &SubjectResult{SchemaName: schemaName, AffectedIDs: []string{}}

	tables, err := compliance.ListTenantTables(ctx, pool, schemaName)
	if err != nil {
		return nil, fmt.Errorf("list tenant tables: %w", err)
	}
	refs, err := compliance.DiscoverUserTables(ctx, pool, schemaName)
	if err != nil {
		return nil, fmt.Errorf("discover user tables: %w", err)
	}
	result.TablesProbed = len(tables) + len(refs)

	err = edb.RunAsService(ctx, pool, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, countQ, args...).Scan(&result.Count); err != nil {
			return fmt.Errorf("count affected subjects: %w", err)
		}
		if limit == 0 {
			return nil
		}
		rows, err := tx.Query(ctx, usersQ, args...)
		if err != nil {
			return fmt.Errorf("query affected subjects: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			result.AffectedIDs = append(result.AffectedIDs, id)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// buildUserWhere assembles the WHERE clause for the users-table count and
// sample queries from a SubjectQuery. Returns the SQL fragment (starting
// with " WHERE", or empty) and positional args.
func buildUserWhere(q SubjectQuery) (string, []interface{}) {
	var conds []string
	var args []interface{}
	idx := 1

	if q.CreatedFrom != nil {
		conds = append(conds, fmt.Sprintf("created_at >= $%d", idx))
		args = append(args, q.CreatedFrom.UTC())
		idx++
	}
	if q.CreatedUntil != nil {
		conds = append(conds, fmt.Sprintf("created_at <= $%d", idx))
		args = append(args, q.CreatedUntil.UTC())
		idx++
	}
	if q.UpdatedFrom != nil {
		conds = append(conds, fmt.Sprintf("updated_at >= $%d", idx))
		args = append(args, q.UpdatedFrom.UTC())
		idx++
	}
	if q.UpdatedUntil != nil {
		conds = append(conds, fmt.Sprintf("updated_at <= $%d", idx))
		args = append(args, q.UpdatedUntil.UTC())
		idx++
	}
	if len(q.UserIDs) > 0 {
		conds = append(conds, fmt.Sprintf("id::text = ANY($%d)", idx))
		args = append(args, q.UserIDs)
		idx++
	}

	if len(conds) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
