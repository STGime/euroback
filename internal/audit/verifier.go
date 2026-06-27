package audit

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// isNoRows is a tiny helper so the Verify path doesn't drag pgx symbols
// into every error-shape check site.
func isNoRows(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}

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
//
// Retention pruning (#171): if the retention worker has pruned the oldest
// rows for this project, a checkpoint row in `audit_log_chain_checkpoints`
// records the row_hash of the last pruned row. Verify seeds its initial
// `prev` from that hash so the first surviving row's `prev_hash` still
// links correctly. Without the checkpoint we'd flag the surviving prefix
// as a chain break — but it's the off-box WORM dump (#170) that holds the
// pruned prefix, so the in-DB chain stays continuous from the checkpoint
// forward.
func (s *Service) Verify(ctx context.Context, projectID string) (*VerifyResult, error) {
	// Seed prev from the retention checkpoint if one exists. NULL-project
	// chains use the all-zero UUID by convention (see migration 000070).
	var prev []byte
	var ckErr error
	if projectID == "" {
		ckErr = s.pool.QueryRow(ctx,
			`SELECT last_row_hash FROM public.audit_log_chain_checkpoints
			 WHERE project_id = '00000000-0000-0000-0000-000000000000'::uuid`).Scan(&prev)
	} else {
		ckErr = s.pool.QueryRow(ctx,
			`SELECT last_row_hash FROM public.audit_log_chain_checkpoints
			 WHERE project_id = NULLIF($1,'')::uuid`, projectID).Scan(&prev)
	}
	if ckErr != nil && !isNoRows(ckErr) {
		return nil, fmt.Errorf("read retention checkpoint: %w", ckErr)
	}

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
