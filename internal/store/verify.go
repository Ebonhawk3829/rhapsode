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
	"encoding/json"
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
//
// The verdict replaces the score-time reasons: the row's evidence becomes
// what verify found, not what the scorer predicted. Without this the
// rejected row shows green ticks for a file that failed — exactly the
// "zero reason why it was rejected" confusion.
func (s *Store) RejectConfirmedCandidate(jobID, id string, verdict []string) error {
	reasons, err := json.Marshal(verdict)
	if err != nil {
		return fmt.Errorf("marshal verdict: %w", err)
	}
	res, err := s.db.Exec(`
		UPDATE job_candidate SET rejected_at = ?, reasons_json = ?
		WHERE job_id = ? AND id = ? AND confirmed_at != ''`,
		ts(time.Now().UTC()), string(reasons), jobID, id)
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

// Orphan is one unclaimed download directory: bytes on disk the pipeline
// will never claim on its own. It lives in store (not job) so both the
// runner and the web layer share the type without an import cycle.
type Orphan struct {
	// Slug is the directory name, usually a job slug from an earlier run.
	Slug string
	// Files are the audio members found inside, basenames only.
	Files []string
	// SizeBytes totals the audio members.
	SizeBytes int64
}

// SaveVerifyWarnings persists the latest inspection's reasons on the job
// row for the tag-review screen. Verify advises; the warnings are display,
// not state.
func (s *Store) SaveVerifyWarnings(jobID string, reasons []string) error {
	raw, err := json.Marshal(reasons)
	if err != nil {
		return fmt.Errorf("marshal warnings: %w", err)
	}
	if _, err := s.db.Exec(`UPDATE job SET verify_json = ? WHERE id = ?`, string(raw), jobID); err != nil {
		return fmt.Errorf("save warnings for %s: %w", jobID, err)
	}
	return nil
}

// VerifyWarnings returns the latest inspection's reasons, or nil.
func (s *Store) VerifyWarnings(jobID string) ([]string, error) {
	var raw string
	if err := s.db.QueryRow(`SELECT verify_json FROM job WHERE id = ?`, jobID).Scan(&raw); err != nil {
		return nil, err
	}
	if raw == "" {
		return nil, nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	return out, nil
}
