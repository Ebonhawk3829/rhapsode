// Package abb is AudioBookBay as a rhapsode source.
//
// A torrent source differs from the slskd source in what one offer is: a
// torrent's file list is fixed by its .torrent, so the offer's "files" are
// whatever the torrent contains — the user confirms the whole torrent or
// nothing. Per-file selection stays meaningful only for shares.
package abb

import (
	"context"
	"errors"
	"time"

	"github.com/Ebonhawk3829/rhapsode/internal/store"

	"github.com/Ebonhawk3829/rhapsode/internal/source"
)

// Source finds audiobook torrents on AudioBookBay.
type Source struct {
	client *Client
}

// NewSource builds a Source over an existing client.
func NewSource(c *Client) *Source { return &Source{client: c} }

// Name identifies the source in logs and the UI.
func (s *Source) Name() string { return "audiobookbay" }

// Kind is the transport slskd handles use — torrents.
func (s *Source) Kind() source.Kind { return source.Torrent }

// Policy is how hard ABB may be pushed.
//
// ABB is bot-hostile and FlareSolverr solves once per challenge, so the
// limits are conservative: one search at a time, seconds between requests.
func (s *Source) Policy() source.Policy {
	return source.Policy{
		Rate:        3 * time.Second,
		Burst:       1,
		Concurrency: 1,
		Backoff:     10 * time.Second,
		Cooldown:    5 * time.Minute,
	}
}

// Search turns listings into candidates.
//
// A listing's format/bitrate/size are catalogue data, not peer text, and the
// info hash is only on the detail page — so the candidate carries the listing
// and resolves its magnet at Grab time, keeping search a single request per
// page.
func (s *Source) Search(ctx context.Context, rel source.Release) ([]source.Candidate, error) {
	query := rel.Title
	if rel.Author != "" {
		query = rel.Title + " " + rel.Author
	}
	listings, err := s.client.Search(ctx, query)
	if err != nil {
		return nil, err
	}
	out := make([]source.Candidate, 0, len(listings))
	for _, l := range listings {
		out = append(out, l.candidate())
	}
	return out, nil
}

// magnetRef is the source-side handle: what page to resolve and what the
// torrent calls itself.
type magnetRef struct {
	DetailPath string
	Title      string
	Files      []source.File // unknown until the torrent is added; listed for verify
}

// Grab resolves the listing into a magnet URI.
func (s *Source) Grab(ctx context.Context, c source.Candidate) (source.Handle, error) {
	ref, ok := c.Handle.Ref.(magnetRef)
	if !ok {
		return source.Handle{}, errors.New("abb: handle is not an ABB magnet reference")
	}
	title := ref.Title
	if c.Title != "" {
		title = c.Title
	}
	magnet, err := s.client.Magnet(ctx, listing{
		DetailPath:  ref.DetailPath,
		Title:       title,
		Format:      c.Format,
		BitrateKbps: c.Bitrate,
	})
	if err != nil {
		return source.Handle{}, err
	}
	return source.Handle{Kind: source.Torrent, Ref: magnetHandle(magnet)}, nil
}

// magnetHandle wraps a magnet URI as a torrent handle.
type magnetHandle string

// String reads the magnet URI back out.
func (m magnetHandle) String() string { return string(m) }

// Rebuild reconstructs a handle for a candidate restored from the store: the
// magnet is re-resolved at Grab time because the hash lives on the detail
// page, so the persisted row keeps the page path, not a hash snapshot.
// files[0].Name is the persisted DetailPath.
func (s *Source) Rebuild(username string, files []store.CandidateFile) source.Handle {
	path := ""
	if len(files) > 0 {
		path = files[0].Name
	}
	return source.Handle{
		Kind: source.Torrent,
		Ref:  magnetRef{DetailPath: path, Title: username},
	}
}

// candidate converts a listing into a source.Candidate.
func (l listing) candidate() source.Candidate {
	// The "file list" of a torrent offer is one entry naming the detail
	// page; per-file selection does not apply to torrents and the path is
	// what lets a persisted candidate re-resolve its magnet later.
	files := []source.File{{Name: l.DetailPath}}
	return source.Candidate{
		Source:    "audiobookbay",
		Title:     l.Title,
		Format:    l.Format,
		Bitrate:   l.BitrateKbps,
		SizeBytes: l.SizeMB * 1024 * 1024,
		Files:     files,
		RemoteDir: l.Posted,
		Handle: source.Handle{
			Kind: source.Torrent,
			Ref:  magnetRef{DetailPath: l.DetailPath, Title: l.Title, Files: files},
		},
	}
}
