package audit

import (
	"bytes"
	"context"
	"fmt"
)

// VerifyResult is the outcome of walking a project's audit-log hash chain.
type VerifyResult struct {
	OK         bool   `json:"ok"`
	Checked    int    `json:"checked"`                // number of rows walked
	BrokenAtID string `json:"broken_at_id,omitempty"` // first row whose integrity failed
	Reason     string `json:"reason,omitempty"`       // why it failed
}

// Verify walks a project's audit-log hash chain in order and reports whether
// it is intact. It performs two independent checks per row:
//
//  1. Content: the stored row_hash must equal audit_row_hash recomputed from
//     the row's own columns + its stored prev_hash. A mismatch means a field
//     was altered.
//  2. Linkage: the row's prev_hash must equal the previous row's row_hash. A
//     mismatch means a row was inserted, deleted, or reordered.
//
// Either failure is reported with the offending row id. An empty chain (no
// rows) verifies as OK. The recompute happens in SQL via the same
// public.audit_row_hash function used on insert, so the comparison is exact.
func (s *Service) Verify(ctx context.Context, projectID string) (*VerifyResult, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, prev_hash, row_hash,
		        public.audit_row_hash(prev_hash, project_id, actor_id, actor_email, action,
		                              target_type, target_id, metadata, ip_address, created_at) AS recomputed
		 FROM public.audit_log
		 WHERE project_id IS NOT DISTINCT FROM NULLIF($1,'')::uuid
		 ORDER BY seq ASC`,
		projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("query audit chain: %w", err)
	}
	defer rows.Close()

	var prev []byte // previous row's row_hash; nil before the first row
	checked := 0
	for rows.Next() {
		var id string
		var prevHash, rowHash, recomputed []byte
		if err := rows.Scan(&id, &prevHash, &rowHash, &recomputed); err != nil {
			return nil, fmt.Errorf("scan audit chain row: %w", err)
		}
		checked++

		// 1. Content integrity.
		if !bytes.Equal(recomputed, rowHash) {
			return &VerifyResult{OK: false, Checked: checked, BrokenAtID: id,
				Reason: "row hash mismatch — a field was altered"}, nil
		}
		// 2. Chain linkage. bytes.Equal treats nil and empty as equal, which
		// is correct for the first row (prev_hash IS NULL, prev is nil).
		if !bytes.Equal(prevHash, prev) {
			return &VerifyResult{OK: false, Checked: checked, BrokenAtID: id,
				Reason: "chain link mismatch — a row was inserted, deleted, or reordered"}, nil
		}
		prev = rowHash
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate audit chain: %w", err)
	}

	return &VerifyResult{OK: true, Checked: checked}, nil
}
