// Package request turns a release the user picked into a durable job.
//
// It sits between the web layer and the job runner because the two talk about
// different things: the UI knows a work and an edition; the runner knows a job
// with a staging slug. This package is where the slug gets coined — the one
// place that knows it must be deterministic (an interrupted job is identified
// by the files in its directory) and collision-free (two works can share a
// title; the same release requested twice must not share a directory).
package request

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/Ebonhawk3829/rhapsode/internal/store"
)

// Creator builds jobs.
type Creator struct {
	st *store.Store
}

// New builds a Creator.
func New(st *store.Store) *Creator {
	return &Creator{st: st}
}

// Request validates the release and stores a job for it.
//
// Validation is thin on purpose: the runner re-derives everything it needs
// from the same fields, so accepting a half-filled release here sticks the
// user with a job that searches for nothing. Title is the only hard floor;
// identifier is required when there is one to name.
//
// A second live job for the same release is refused: the retry path for a
// failed download is the confirm screen's next candidate, never a second
// job, so a duplicate request is user error. The error names the existing
// job so the UI can link it.
func (c *Creator) Request(rel Release) (*store.Job, error) {
	if strings.TrimSpace(rel.Title) == "" {
		return nil, fmt.Errorf("title is required")
	}

	slug := Slug(rel)
	if existing, err := c.liveJobForSlug(slug); err != nil {
		return nil, err
	} else if existing != nil {
		return nil, fmt.Errorf("a live job for this release already exists (job %s, state %q)", existing.ID, existing.State)
	}

	j := &store.Job{
		Title:          strings.TrimSpace(rel.Title),
		Author:         strings.TrimSpace(rel.Author),
		Series:         strings.TrimSpace(rel.Series),
		SeriesSequence: strings.TrimSpace(rel.SeriesSequence),
		Narrator:       strings.TrimSpace(rel.Narrator),
		Publisher:      strings.TrimSpace(rel.Publisher),
		PublishedYear:  strings.TrimSpace(rel.PublishedYear),
		Description:    strings.TrimSpace(rel.Description),
		Genre:          strings.TrimSpace(rel.Genre),
		IdentifierKind: string(rel.Identifier.Kind),
		IdentifierVal:  rel.Identifier.Value,
		Query:          strings.TrimSpace(rel.Query),
		Slug:           slug,
	}
	if err := c.st.Create(j); err != nil {
		return nil, fmt.Errorf("create job: %w", err)
	}
	return j, nil
}

// liveJobForSlug returns the non-terminal job holding a slug, or nil when
// the slug is free. Terminal jobs (done/failed/cancelled) do not block:
// re-requesting after a terminal outcome starts fresh in the same directory.
func (c *Creator) liveJobForSlug(slug string) (*store.Job, error) {
	jobs, err := c.st.List("")
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	for _, j := range jobs {
		if j.Slug == slug && !j.State.Terminal() {
			return j, nil
		}
	}
	return nil, nil
}

// Release is the pick the UI submits: one edition of one work.
type Release struct {
	Title          string
	Author         string
	Series         string
	SeriesSequence string
	Narrator       string
	Publisher      string
	// PublishedYear is the bare YYYY the catalogue reported; it rides the
	// job so the tagger can write the library contract's publishing date.
	PublishedYear string
	// Description is the publisher's blurb; it rides the job so the tagger
	// can write the library contract's description.
	Description string
	// Genre is the catalogue's genre for the release, for the tagger.
	Genre      string
	Identifier ID
	// Query is the raw free-text the user typed, kept so catalogue-less
	// requests still have something to search with.
	Query string
}

// ID is the release's catalogue key.
type ID struct {
	Kind  string
	Value string
}

// Slug is the deterministic staging directory name for a release.
//
// Shape: `the-black-prism-9781648794384` — a human-readable prefix (so an
// `ls` means something) plus a short hash of the identity (so identical
// titles never collide and the same release always lands in the same
// directory). Catalogue-less requests hash the query text instead, which
// keeps them resumable too.
func Slug(rel Release) string {
	base := slugify(rel.Title)
	if base == "" {
		base = "request"
	}

	seed := rel.Identifier.Kind + ":" + rel.Identifier.Value
	if seed == ":" {
		seed = "query:" + strings.ToLower(strings.TrimSpace(rel.Query))
	}
	sum := sha256.Sum256([]byte(seed))
	return base + "-" + hex.EncodeToString(sum[:])[:8]
}

// slugify reduces a title to a filesystem-safe token.
func slugify(s string) string {
	var b strings.Builder
	b.Grow(len(s))

	lastDash := true // leading dashes are suppressed
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}
