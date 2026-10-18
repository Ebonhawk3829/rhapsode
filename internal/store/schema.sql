-- rhapsode schema.
--
-- Every request is a durable job: created, resumable, inspectable and
-- replayable after a restart. Acquisition can take hours, so job state has to
-- survive the process that started it.

CREATE TABLE IF NOT EXISTS schema_version (
    version INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS job (
    id            TEXT PRIMARY KEY,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    state         TEXT NOT NULL,

    -- The release being acquired.
    title         TEXT NOT NULL,
    author        TEXT NOT NULL DEFAULT '',
    series        TEXT NOT NULL DEFAULT '',
    series_seq    TEXT NOT NULL DEFAULT '',
    narrator      TEXT NOT NULL DEFAULT '',
    publisher     TEXT NOT NULL DEFAULT '',
    published_year TEXT NOT NULL DEFAULT '',
    id_kind       TEXT NOT NULL DEFAULT '',
    id_value      TEXT NOT NULL DEFAULT '',
    query         TEXT NOT NULL DEFAULT '',

    -- Where it is being acquired from.
    slug          TEXT NOT NULL,
    source        TEXT NOT NULL DEFAULT '',
    grabber       TEXT NOT NULL DEFAULT '',
    grab_id       TEXT NOT NULL DEFAULT '',

    -- Progress.
    phase         TEXT NOT NULL DEFAULT '',
    progress      REAL NOT NULL DEFAULT 0,
    attempts      INTEGER NOT NULL DEFAULT 0,
    error         TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS job_state_idx ON job (state);
CREATE INDEX IF NOT EXISTS job_slug_idx ON job (slug);

-- The audit trail: what was requested, from where, and what landed.
CREATE TABLE IF NOT EXISTS job_event (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id     TEXT NOT NULL REFERENCES job (id) ON DELETE CASCADE,
    at         TEXT NOT NULL,
    kind       TEXT NOT NULL,
    message    TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS job_event_job_idx ON job_event (job_id, at);

-- One offer from one source to satisfy one job: a peer sharing a folder of
-- files (slskd), a torrent (ABB). The unit of user decision is one row: the
-- confirm screen lists these, ordered by score, and the user picks files
-- from one.
--
-- Rows are persisted at search time so confirming can enqueue the recorded
-- file set without a re-search, and so the evidence the scorer found is
-- still on screen after a restart.
CREATE TABLE IF NOT EXISTS job_candidate (
    id          TEXT PRIMARY KEY,
    job_id      TEXT NOT NULL REFERENCES job (id) ON DELETE CASCADE,
    created_at  TEXT NOT NULL,

    -- Where the offer came from.
    source      TEXT NOT NULL,
    kind        TEXT NOT NULL DEFAULT '',
    username    TEXT NOT NULL DEFAULT '',

    -- What the source presents, verbatim.
    title       TEXT NOT NULL DEFAULT '',
    remote_dir  TEXT NOT NULL DEFAULT '',
    file_json   TEXT NOT NULL DEFAULT '[]',

    -- Summarised from the file list for ranking and display.
    size_bytes  INTEGER NOT NULL DEFAULT 0,
    format      TEXT NOT NULL DEFAULT '',
    bitrate     INTEGER NOT NULL DEFAULT 0,
    upload_speed_kbps INTEGER NOT NULL DEFAULT 0,
    free_slot   INTEGER NOT NULL DEFAULT 0,

    -- What the scorer thought. Score orders the list; reasons are the
    -- evidence lines shown under each row. Bands never gate: at most they
    -- reorder, and every candidate that exists stays visible.
    score       REAL NOT NULL DEFAULT 0,
    band        TEXT NOT NULL DEFAULT '',
    reasons_json TEXT NOT NULL DEFAULT '[]',

    -- The user's decision on this row.
    rejected_at  TEXT NOT NULL DEFAULT '',
    confirmed_at TEXT NOT NULL DEFAULT '',
    selected_json TEXT NOT NULL DEFAULT '',  -- files the user confirmed

    -- Re-running a search is expected; the same offer twice is not. The
    -- conflict target makes SaveCandidate idempotent without resurrecting
    -- decisions (rejected stays rejected, confirmed keeps its selection).
    UNIQUE (job_id, source, username, remote_dir)
);

CREATE INDEX IF NOT EXISTS job_candidate_job_idx
    ON job_candidate (job_id, band, score DESC);
