// Candidate persistence: the offers a search pass produced, and the user's
// decision on each.
//
// One row is one offer from one source — a peer sharing a folder (slskd), a
// torrent (ABB). Rows are written at search time so that confirming can
// enqueue the recorded file set without a re-search, and so the evidence the
// scorer found survives a restart. Decisions are per job by construction: a
// candidate rejected in one job says nothing about the same share offered to
// another job scoring against a different release.
package store

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// CandidateFile is one file inside a candidate's offering, as recorded at
// search time and re-usable at enqueue time.
type CandidateFile struct {
	Name string `json:"name"` // full source-side path
	Size int64  `json:"size"`
}

// Candidate is one persisted offer.
type Candidate struct {
	ID        string
	JobID     string
	CreatedAt time.Time

	Source   string
	Kind     string
	Username string

	Title       string
	RemoteDir   string
	ExternalURL string
	Files       []CandidateFile

	SizeBytes       int64
	Format          string
	Bitrate         int
	UploadSpeedKbps int
	FreeSlot        bool

	// Score orders the confirm screen; band and Reasons annotate it. Bands
	// never gate: they reorder at most, and every candidate found is shown.
	Score   float64
	Band    string
	Reasons []string

	// The decision. RejectedAt and ConfirmedAt are plain timestamps, not a
	// state: the interesting questions ("was this taken or turned down?",
	// "did it later fail?") are answered by which timestamp is set.
	RejectedAt  time.Time
	ConfirmedAt time.Time

	// SelectedFiles is the subset the user confirmed. Empty when not
	// confirmed; recording the subset rather than assuming the whole offer
	// is what makes per-file selection possible.
	SelectedFiles []CandidateFile
}

// SaveCandidate inserts a candidate, or ignores it if the same offer from the
// same source is already recorded for the job.
//
// Re-running a search is expected: the user may reject the best offer and ask
// for more. Ignoring duplicates keeps decisions intact — a re-found offer
// that was already rejected stays rejected within its job, and one already
// confirmed keeps its selection rather than being overwritten by a later
// sweep.
func (s *Store) SaveCandidate(c *Candidate) error {
	if c.ID == "" {
		c.ID = uuid.NewString()
	}
	files, err := json.Marshal(c.Files)
	if err != nil {
		return fmt.Errorf("marshal files: %w", err)
	}
	reasons, err := json.Marshal(c.Reasons)
	if err != nil {
		return fmt.Errorf("marshal reasons: %w", err)
	}
	_, err = s.db.Exec(`
		INSERT INTO job_candidate (
			id, job_id, created_at,
			source, kind, username,
			title, remote_dir, external_url, file_json,
			size_bytes, format, bitrate, upload_speed_kbps, free_slot,
			score, band, reasons_json,
			rejected_at, confirmed_at, selected_json
		) VALUES (?,?,?, ?,?,?, ?,?,?,?, ?,?,?,?,?,?,?,?, '','','[]')
		ON CONFLICT (job_id, source, username, remote_dir) DO NOTHING`,
		c.ID, c.JobID, ts(time.Now().UTC()),
		c.Source, c.Kind, c.Username,
		c.Title, c.RemoteDir, c.ExternalURL, string(files),
		c.SizeBytes, c.Format, c.Bitrate, c.UploadSpeedKbps, c.FreeSlot,
		c.Score, c.Band, string(reasons),
	)
	if err != nil {
		return fmt.Errorf("save candidate: %w", err)
	}
	return nil
}

// Candidates returns a job's persisted candidates, best first.
//
// The limit is a display filter (show the top N by score), never a gate: a
// small search whose every result is poorly scored still shows everything it
// found. Zero means no limit.
func (s *Store) Candidates(jobID string, limit int) ([]*Candidate, error) {
	q := `
		SELECT id, job_id, created_at,
		       source, kind, username,
		       title, remote_dir, external_url, file_json,
		       size_bytes, format, bitrate, upload_speed_kbps, free_slot,
		       score, band, reasons_json,
		       rejected_at, confirmed_at, selected_json
		FROM job_candidate`
	// Rejected rows are hidden until every row is rejected — the job page
	// then shows why the job has nothing left to offer. Confirmed rows are
	// always kept: they are the historical record of what was taken.
	q += ` WHERE job_id = ? AND (rejected_at = '' OR confirmed_at != '')`
	args := []any{jobID}
	if limit > 0 {
		q += ` ORDER BY confirmed_at DESC, score DESC LIMIT ?`
		args = append(args, limit)
	} else {
		q += ` ORDER BY confirmed_at DESC, score DESC`
	}
	return s.queryCandidates(q, args...)
}

// CandidateByID returns one candidate by ID.
func (s *Store) CandidateByID(jobID, id string) (*Candidate, error) {
	rows, err := s.queryCandidates(`
		SELECT id, job_id, created_at,
		       source, kind, username,
		       title, remote_dir, external_url, file_json,
		       size_bytes, format, bitrate, upload_speed_kbps, free_slot,
		       score, band, reasons_json,
		       rejected_at, confirmed_at, selected_json
		FROM job_candidate
		WHERE job_id = ? AND id = ?`, jobID, id)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, ErrNotFound
	}
	return rows[0], nil
}

// CandidateCount reports how many candidates a job still has on offer.
//
// Only genuinely selectable rows count: a rejected row is off the table
// even when it carries a confirmation stamp (the failed-verify record —
// visible on the job page, but not re-confirmable and not an offer).
func (s *Store) CandidateCount(jobID string) (int, error) {
	n := 0
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM job_candidate
		WHERE job_id = ? AND rejected_at = ''`,
		jobID).Scan(&n)
	return n, err
}

// Rejected returns a job's ruled-out offers, most recently rejected first.
// These are the verify verdicts and user rejections: visible history with
// their evidence, each restorable via Unreject.
func (s *Store) Rejected(jobID string) ([]*Candidate, error) {
	return s.queryCandidates(`
		SELECT id, job_id, created_at,
		       source, kind, username,
		       title, remote_dir, external_url, file_json,
		       size_bytes, format, bitrate, upload_speed_kbps, free_slot,
		       score, band, reasons_json,
		       rejected_at, confirmed_at, selected_json
		FROM job_candidate
		WHERE job_id = ? AND rejected_at != ''
		ORDER BY rejected_at DESC`, jobID)
}

// RejectCandidate records the user's rejection of one candidate.
//
// Rejection is per job and sticky: the row stays in the database with its
// evidence, so the audit trail can answer "what did the user see and turn
// down", but it no longer appears on the confirm screen.
func (s *Store) RejectCandidate(jobID, id string) error {
	res, err := s.db.Exec(`
		UPDATE job_candidate SET rejected_at = ?
		WHERE job_id = ? AND id = ? AND confirmed_at = ''`,
		ts(time.Now().UTC()), jobID, id)
	if err != nil {
		return fmt.Errorf("reject candidate %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("reject candidate %s: %w", id, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ConfirmCandidate records the user's file selection on one candidate.
//
// The selected files are the whole truth of what was asked to download: the
// enqueue uses them, and verification later compares against them. Selecting
// a subset leaves the row otherwise untouched, so the offer's full evidence
// remains available.
func (s *Store) ConfirmCandidate(jobID, id string, selected []CandidateFile) error {
	files, err := json.Marshal(selected)
	if err != nil {
		return fmt.Errorf("marshal selection: %w", err)
	}
	res, err := s.db.Exec(`
		UPDATE job_candidate
		SET confirmed_at = ?, selected_json = ?, rejected_at = ''
		WHERE job_id = ? AND id = ? AND rejected_at = ''`,
		ts(time.Now().UTC()), string(files), jobID, id)
	if err != nil {
		return fmt.Errorf("confirm candidate %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("confirm candidate %s: %w", id, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) queryCandidates(q string, args ...any) ([]*Candidate, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("query candidates: %w", err)
	}
	defer rows.Close()

	var out []*Candidate
	for rows.Next() {
		var (
			c            Candidate
			created      string
			filesJSON    string
			reasonsJSON  string
			rejected     string
			confirmed    string
			selectedJSON string
			freeSlot     int
		)
		if err := rows.Scan(
			&c.ID, &c.JobID, &created,
			&c.Source, &c.Kind, &c.Username,
			&c.Title, &c.RemoteDir, &c.ExternalURL, &filesJSON,
			&c.SizeBytes, &c.Format, &c.Bitrate, &c.UploadSpeedKbps, &freeSlot,
			&c.Score, &c.Band, &reasonsJSON,
			&rejected, &confirmed, &selectedJSON,
		); err != nil {
			return nil, fmt.Errorf("scan candidate: %w", err)
		}
		c.FreeSlot = freeSlot != 0

		if c.Files, err = decodeFiles(filesJSON); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(reasonsJSON), &c.Reasons); err != nil {
			return nil, fmt.Errorf("decode reasons: %w", err)
		}
		if selectedJSON != "" && selectedJSON != "[]" {
			if c.SelectedFiles, err = decodeFiles(selectedJSON); err != nil {
				return nil, err
			}
		}
		if c.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		c.RejectedAt, _ = parseTime(rejected)   // empty stays zero
		c.ConfirmedAt, _ = parseTime(confirmed) // empty stays zero

		out = append(out, &c)
	}
	return out, rows.Err()
}

func decodeFiles(s string) ([]CandidateFile, error) {
	if s == "" || s == "[]" {
		return nil, nil
	}
	var files []CandidateFile
	if err := json.Unmarshal([]byte(s), &files); err != nil {
		return nil, fmt.Errorf("decode files: %w", err)
	}
	return files, nil
}
