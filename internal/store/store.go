// Package store persists jobs and their audit trail in SQLite.
//
// Every request is a durable job. Acquisition can take hours, so job state has
// to survive the process that started it: a job is created, resumed,
// inspected and replayed across restarts.
package store

import (
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

// ErrNotFound is returned when a job does not exist.
var ErrNotFound = errors.New("not found")

// Store wraps the database.
type Store struct {
	db *sql.DB
}

// Open opens (or creates) the database at path and applies the schema.
//
// busy_timeout matters: the job runner and the UI both write, and SQLite's
// default behaviour on contention is to fail immediately rather than wait.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	// SQLite is a single-writer database; more connections only create
	// contention.
	db.SetMaxOpenConns(1)

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	// CREATE TABLE IF NOT EXISTS cannot widen an existing table, and jobs
	// from before candidate confirmation still open cleanly. The columns
	// are additive; detect once and alter rather than version the schema
	// the job built on.
	cols, err := s.db.Query(`PRAGMA table_info(job)`)
	if err != nil {
		return fmt.Errorf("inspect job table: %w", err)
	}
	defer cols.Close()
	have := map[string]bool{}
	for cols.Next() {
		var (
			cid, name, ctype string
			notnull, pk      int
			dflt             sql.NullString
		)
		if err := cols.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return fmt.Errorf("inspect job table: %w", err)
		}
		have[name] = true
	}
	if err := cols.Err(); err != nil {
		return fmt.Errorf("inspect job table: %w", err)
	}
	for _, col := range []string{"confirmed_candidate_id", "published_year", "description", "genre", "verify_json"} {
		if !have[col] {
			if _, err := s.db.Exec(
				`ALTER TABLE job ADD COLUMN ` + col + ` TEXT NOT NULL DEFAULT ''`); err != nil {
				return fmt.Errorf("add %s: %w", col, err)
			}
		}
	}
	// The candidate table pre-dates its uniqueness constraint in databases
	// created while only job carried new columns. SQLite cannot add a
	// constraint in place; rebuild the table when the constraint is absent.
	if err := s.rebuildCandidates(); err != nil {
		return err
	}
	// Additive column for existing new-shape tables: the rebuild above only
	// fires when the constraint is absent, so a table with the constraint
	// but without external_url needs the plain ALTER path.
	if err := s.addCandidateColumn("external_url"); err != nil {
		return err
	}
	return nil
}

// addCandidateColumn adds a TEXT column to job_candidate when absent.
func (s *Store) addCandidateColumn(col string) error {
	cols, err := s.db.Query(`PRAGMA table_info(job_candidate)`)
	if err != nil {
		return fmt.Errorf("inspect job_candidate: %w", err)
	}
	defer cols.Close()
	for cols.Next() {
		var (
			cid, name, ctype string
			notnull, pk      int
			dflt             sql.NullString
		)
		if err := cols.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return fmt.Errorf("inspect job_candidate: %w", err)
		}
		if name == col {
			return cols.Err()
		}
	}
	if err := cols.Err(); err != nil {
		return fmt.Errorf("inspect job_candidate: %w", err)
	}
	if _, err := s.db.Exec(
		`ALTER TABLE job_candidate ADD COLUMN ` + col + ` TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("add %s: %w", col, err)
	}
	return nil
}

// rebuildCandidates re-creates job_candidate with its uniqueness constraint
// when an older shape of the table is found. Rows are carried across; an
// empty new-shape table means nothing to migrate.
func (s *Store) rebuildCandidates() error {
	var n int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='job_candidate'`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return nil // created fresh by the schema just applied
	}
	var hasUnique int
	// The column list of a UNIQUE table constraint appears in the DDL; the
	// idempotent check is whether the constraint name is findable.
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM pragma_index_list('job_candidate') WHERE origin = 'u'`).Scan(&hasUnique); err != nil {
		return err
	}
	if hasUnique > 0 {
		return nil
	}
	if _, err := s.db.Exec(`
		ALTER TABLE job_candidate RENAME TO job_candidate_old;
		CREATE TABLE job_candidate AS SELECT * FROM job_candidate_old; -- replaced below
		DROP TABLE job_candidate_old;`); err == nil {
		// Fallthrough: replaced by the definitive rebuild below.
		_ = err
	}
	// Definitive rebuild: full column set with the UNIQUE constraint.
	stmts := []string{
		`DROP TABLE IF EXISTS job_candidate_rebuild`,
		`CREATE TABLE job_candidate_rebuild (
			id          TEXT PRIMARY KEY,
			job_id      TEXT NOT NULL REFERENCES job (id) ON DELETE CASCADE,
			created_at  TEXT NOT NULL,
			source      TEXT NOT NULL,
			kind        TEXT NOT NULL DEFAULT '',
			username    TEXT NOT NULL DEFAULT '',
			title       TEXT NOT NULL DEFAULT '',
			remote_dir  TEXT NOT NULL DEFAULT '',
			external_url TEXT NOT NULL DEFAULT '',
			file_json   TEXT NOT NULL DEFAULT '[]',
			size_bytes  INTEGER NOT NULL DEFAULT 0,
			format      TEXT NOT NULL DEFAULT '',
			bitrate     INTEGER NOT NULL DEFAULT 0,
			upload_speed_kbps INTEGER NOT NULL DEFAULT 0,
			free_slot   INTEGER NOT NULL DEFAULT 0,
			score       REAL NOT NULL DEFAULT 0,
			band        TEXT NOT NULL DEFAULT '',
			reasons_json TEXT NOT NULL DEFAULT '[]',
			rejected_at  TEXT NOT NULL DEFAULT '',
			confirmed_at TEXT NOT NULL DEFAULT '',
			selected_json TEXT NOT NULL DEFAULT '',
			UNIQUE (job_id, source, username, remote_dir)
		)`,
		`INSERT OR IGNORE INTO job_candidate_rebuild SELECT
			id, job_id, created_at, source, kind, username,
			title, remote_dir, '', file_json, size_bytes, format, bitrate,
			upload_speed_kbps, free_slot, score, band, reasons_json,
			rejected_at, confirmed_at, selected_json
		FROM job_candidate`,
		`DROP TABLE job_candidate`,
		`ALTER TABLE job_candidate_rebuild RENAME TO job_candidate`,
		`CREATE INDEX IF NOT EXISTS job_candidate_job_idx ON job_candidate (job_id, band, score DESC)`,
	}
	for _, q := range stmts {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("rebuild job_candidate: %w", err)
		}
	}
	return nil
}

// State is the lifecycle position of a job.
type State string

const (
	// Pending means created but not started.
	Pending State = "pending"
	// Searching means candidates are being gathered.
	Searching State = "searching"
	// Confirm means the search has finished and a candidate awaits the
	// user's file selection.
	Confirm State = "confirm"
	// Downloading means a candidate is transferring.
	Downloading State = "downloading"
	// Verifying means the download is being checked for completeness.
	Verifying State = "verifying"
	// Merging means parts are being combined into one file.
	Merging State = "merging"
	// TagReview means the merge landed and the proposed tags await the
	// user's edit and confirmation.
	TagReview State = "tag-review"
	// Tagging means metadata is being written.
	Tagging State = "tagging"
	// Shelving means the file is being moved into the library.
	Shelving State = "shelving"
	// Done means the title is in the library.
	Done State = "done"
	// Failed means the job gave up.
	Failed State = "failed"
	// Cancelled means the user abandoned it.
	Cancelled State = "cancelled"
)

// Terminal reports whether a state is an end state that will not change
// without intervention.
func (s State) Terminal() bool {
	return s == Done || s == Failed || s == Cancelled
}

// Job is one acquisition request.
type Job struct {
	ID        string
	CreatedAt time.Time
	UpdatedAt time.Time
	State     State

	// The release being acquired.
	Title          string
	Author         string
	Series         string
	SeriesSequence string
	Narrator       string
	Publisher      string
	PublishedYear  string
	Description    string
	Genre          string
	IdentifierKind string
	IdentifierVal  string
	Query          string

	// Where it is being acquired from.
	Slug    string
	Source  string
	Grabber string
	GrabID  string

	// The candidate the user confirmed, set when the job entered
	// downloading. Historical record of which offer was taken.
	ConfirmedCandidateID string

	// Progress.
	Phase    string
	Progress float64
	Attempts int
	Error    string
}

// Create inserts a job, assigning it an ID and timestamps.
//
// The slug is required: it names the staging directory, so a job without one
// has nowhere to put its download.
func (s *Store) Create(j *Job) error {
	if j.Slug == "" {
		return errors.New("job slug is required")
	}
	if j.ID == "" {
		j.ID = uuid.NewString()
	}
	now := time.Now().UTC()
	if j.CreatedAt.IsZero() {
		j.CreatedAt = now
	}
	j.UpdatedAt = now
	if j.State == "" {
		j.State = Pending
	}

	_, err := s.db.Exec(`
		INSERT INTO job (
			id, created_at, updated_at, state,
			title, author, series, series_seq, narrator, publisher, published_year,
			description, genre,
			id_kind, id_value, query,
			slug, source, grabber, grab_id, confirmed_candidate_id,
			phase, progress, attempts, error
		) VALUES (?,?,?,?, ?,?,?,?,?,?,?, ?,?, ?,?,?, ?,?,?,?,?, ?,?,?,?)`,
		j.ID, ts(j.CreatedAt), ts(j.UpdatedAt), string(j.State),
		j.Title, j.Author, j.Series, j.SeriesSequence, j.Narrator, j.Publisher, j.PublishedYear,
		j.Description, j.Genre,
		j.IdentifierKind, j.IdentifierVal, j.Query,
		j.Slug, j.Source, j.Grabber, j.GrabID, j.ConfirmedCandidateID,
		j.Phase, j.Progress, j.Attempts, j.Error,
	)
	if err != nil {
		return fmt.Errorf("insert job: %w", err)
	}
	return nil
}

// Get returns one job by ID.
func (s *Store) Get(id string) (*Job, error) {
	row := s.db.QueryRow(`
		SELECT id, created_at, updated_at, state,
		       title, author, series, series_seq, narrator, publisher, published_year,
		       description, genre,
		       id_kind, id_value, query,
		       slug, source, grabber, grab_id, confirmed_candidate_id,
		       phase, progress, attempts, error
		FROM job WHERE id = ?`, id)

	j, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get job %s: %w", id, err)
	}
	return j, nil
}

// List returns jobs, newest first. An empty state returns every job.
func (s *Store) List(state State) ([]*Job, error) {
	q := `
		SELECT id, created_at, updated_at, state,
		       title, author, series, series_seq, narrator, publisher, published_year,
		       description, genre,
		       id_kind, id_value, query,
		       slug, source, grabber, grab_id, confirmed_candidate_id,
		       phase, progress, attempts, error
		FROM job`
	args := []any{}
	if state != "" {
		q += " WHERE state = ?"
		args = append(args, string(state))
	}
	q += " ORDER BY created_at DESC"

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	defer rows.Close()

	var out []*Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("scan job: %w", err)
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// Update writes a job's mutable fields back.
//
// The release fields are not updated: a job's identity is fixed at creation,
// and a job that changed which book it was acquiring would be a different job.
// The tag-review step is the one exception: confirming edited tags rewrites
// the release fields via UpdateTags, not here.
func (s *Store) Update(j *Job) error {
	j.UpdatedAt = time.Now().UTC()
	res, err := s.db.Exec(`
		UPDATE job SET
			updated_at = ?, state = ?,
			slug = ?, source = ?, grabber = ?, grab_id = ?,
			confirmed_candidate_id = ?,
			phase = ?, progress = ?, attempts = ?, error = ?
		WHERE id = ?`,
		ts(j.UpdatedAt), string(j.State),
		j.Slug, j.Source, j.Grabber, j.GrabID,
		j.ConfirmedCandidateID,
		j.Phase, j.Progress, j.Attempts, j.Error,
		j.ID,
	)
	if err != nil {
		return fmt.Errorf("update job %s: %w", j.ID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update job %s: %w", j.ID, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateTags rewrites a job's release fields after the tag-review confirm.
// This is the only path that mutates identity post-creation: the user
// reviewed the proposal and confirmed exactly this set.
func (s *Store) UpdateTags(j *Job) error {
	j.UpdatedAt = time.Now().UTC()
	res, err := s.db.Exec(`
		UPDATE job SET
			updated_at = ?, state = ?,
			title = ?, author = ?, series = ?, series_seq = ?,
			narrator = ?, publisher = ?, published_year = ?,
			description = ?, genre = ?,
			phase = ?
		WHERE id = ?`,
		ts(j.UpdatedAt), string(j.State),
		j.Title, j.Author, j.Series, j.SeriesSequence,
		j.Narrator, j.Publisher, j.PublishedYear,
		j.Description, j.Genre,
		j.Phase,
		j.ID,
	)
	if err != nil {
		return fmt.Errorf("update tags %s: %w", j.ID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update tags %s: %w", j.ID, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes a terminal job and its candidates. Only terminal jobs
// (done, failed, cancelled) can be cleared: a live job holds downloads and
// decisions that deletion would orphan.
func (s *Store) Delete(id string) error {
	j, err := s.Get(id)
	if err != nil {
		return err
	}
	if !j.State.Terminal() {
		return fmt.Errorf("job %s is %q, not terminal; cancel it first", id, j.State)
	}
	if _, err := s.db.Exec(`DELETE FROM job_candidate WHERE job_id = ?`, id); err != nil {
		return fmt.Errorf("delete candidates for %s: %w", id, err)
	}
	if _, err := s.db.Exec(`DELETE FROM job_event WHERE job_id = ?`, id); err != nil {
		return fmt.Errorf("delete events for %s: %w", id, err)
	}
	if _, err := s.db.Exec(`DELETE FROM job WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete job %s: %w", id, err)
	}
	return nil
}

// Event is one entry in a job's audit trail.
type Event struct {
	ID      int64
	JobID   string
	At      time.Time
	Kind    string
	Message string
}

// AddEvent appends to a job's audit trail.
func (s *Store) AddEvent(jobID, kind, message string) error {
	_, err := s.db.Exec(
		`INSERT INTO job_event (job_id, at, kind, message) VALUES (?,?,?,?)`,
		jobID, ts(time.Now().UTC()), kind, message,
	)
	if err != nil {
		return fmt.Errorf("add event to %s: %w", jobID, err)
	}
	return nil
}

// Events returns a job's audit trail, oldest first.
func (s *Store) Events(jobID string) ([]Event, error) {
	rows, err := s.db.Query(
		`SELECT id, job_id, at, kind, message FROM job_event WHERE job_id = ? ORDER BY at, id`,
		jobID,
	)
	if err != nil {
		return nil, fmt.Errorf("list events for %s: %w", jobID, err)
	}
	defer rows.Close()

	var out []Event
	for rows.Next() {
		var (
			e  Event
			at string
		)
		if err := rows.Scan(&e.ID, &e.JobID, &at, &e.Kind, &e.Message); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		t, err := parseTime(at)
		if err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		e.At = t
		out = append(out, e)
	}
	return out, rows.Err()
}

// rowScanner is the shared shape of *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanJob(r rowScanner) (*Job, error) {
	var (
		j                Job
		created, updated string
		state            string
	)
	if err := r.Scan(
		&j.ID, &created, &updated, &state,
		&j.Title, &j.Author, &j.Series, &j.SeriesSequence, &j.Narrator, &j.Publisher, &j.PublishedYear,
		&j.Description, &j.Genre,
		&j.IdentifierKind, &j.IdentifierVal, &j.Query,
		&j.Slug, &j.Source, &j.Grabber, &j.GrabID, &j.ConfirmedCandidateID,
		&j.Phase, &j.Progress, &j.Attempts, &j.Error,
	); err != nil {
		return nil, err
	}

	var err error
	if j.CreatedAt, err = parseTime(created); err != nil {
		return nil, err
	}
	if j.UpdatedAt, err = parseTime(updated); err != nil {
		return nil, err
	}
	j.State = State(state)
	return &j, nil
}

// ts formats a time for storage. RFC3339 in UTC sorts lexically, which is what
// the ORDER BY clauses rely on.
func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// parseTime reads a stored timestamp. modernc.org/sqlite hands datetime columns
// back as TEXT, so every one has to be parsed rather than scanned directly.
func parseTime(s string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised timestamp %q", s)
}
