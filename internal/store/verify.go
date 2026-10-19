// Verify persistence: the store operations the verify phase needs beyond the
// plain job/candidate CRUD.
//
// A failed verification parks the job back in confirm with the offending
// candidate rejected (docs/design.md): the user re-engages on the confirm
// screen instead of re-running a search. Rejecting a candidate that was
// already confirmed is a different operation from the confirm-screen
// rejection — the row carries a confirmed_at stamp and a selection, and both
// stay as the historical record of what was tried and failed.
package store

import (
	"fmt"
	"time"
)

// RejectConfirmedCandidate rejects the candidate a job confirmed and
// downloaded, after verification failed.
//
// Unlike RejectCandidate (a confirm-screen decision that only touches
// unconfirmed rows), this stamps the verify verdict on a row that was
// already confirmed. The confirmation stamp stays: it is the historical
// record of what was tried and failed, and the Candidates query keeps
// confirmed rows visible even when rejected — the failed offer remains on
// the job page so the user can see what went wrong. Re-confirmation is
// blocked by the rejected_at guard in ConfirmCandidate, which is the
// default: a candidate that failed verification must not be picked twice
// by accident. Unreject reverses it deliberately (see below).
func (s *Store) RejectConfirmedCandidate(jobID, id string) error {
	res, err := s.db.Exec(`
		UPDATE job_candidate SET rejected_at = ?
		WHERE job_id = ? AND id = ? AND confirmed_at != ''`,
		ts(time.Now().UTC()), jobID, id)
	if err != nil {
		return fmt.Errorf("reject confirmed candidate %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("reject confirmed candidate %s: %w", id, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Unreject clears a candidate's rejected stamp so it can be confirmed again.
//
// This is the deliberate override for a verify verdict the user disagrees
// with: the verifier compared tags and cried wrong, the human read the
// verdict and says the file is fine. It clears rejected_at on confirmed and
// unconfirmed rows alike, and bumps confirmed_at back to empty on reviewed
// rows so the confirm path treats the row as a fresh offer. The audit trail
// keeps the rejection event — the override is recorded, not erased.
func (s *Store) Unreject(jobID, id string) error {
	res, err := s.db.Exec(`
		UPDATE job_candidate SET rejected_at = '', confirmed_at = ''
		WHERE job_id = ? AND id = ?`,
		jobID, id)
	if err != nil {
		return fmt.Errorf("unreject candidate %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("unreject candidate %s: %w", id, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
