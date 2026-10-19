// Package source defines the acquisition side of rhapsode: where copies of a
// release are found, and how one is handed to a downloader.
//
// A Source knows how to find and fetch a handle. It does not know how to track
// bytes — that is the Grabber's job, and the two are deliberately separate so
// that one source can be served by whichever downloader suits its Kind.
package source

import (
	"context"

	"github.com/Ebonhawk3829/rhapsode/internal/metadata"
)

// Source is one place copies of a release can be obtained.
type Source interface {
	// Name identifies the source in logs, in the UI, and on a Candidate. It
	// is stable and lowercase, e.g. "audiobookbay".
	Name() string

	// Kind is the transport this source's handles use. It selects the
	// Grabber: a torrent handle goes to a torrent client, a p2p handle to
	// slskd. The caller routes on this value and never on the source's name.
	Kind() Kind

	// Search returns candidates for a release, best first.
	//
	// The release may carry no metadata at all: a free-text query that
	// matched no catalogue is passed straight through, and the source is
	// expected to search on whatever it has.
	Search(ctx context.Context, release Release) ([]Candidate, error)

	// Grab hands one candidate to the downloader and returns a handle the
	// Grabber can track. slug is the job's staging directory name: the
	// source enqueues under audiobooks/<slug> so downloads land where the
	// rest of the pipeline (and the orphan cleanup) expects them.
	//
	// Grab does not download. It enqueues, and returns as soon as the
	// downloader has accepted the job.
	Grab(ctx context.Context, c Candidate, slug string) (Handle, error)
}

// Kind is the transport a source's handles use.
type Kind string

const (
	// Torrent is a BitTorrent handle, served by a torrent client.
	Torrent Kind = "torrent"
	// P2P is a Soulseek handle, served by slskd.
	P2P Kind = "p2p"
	// Direct is an HTTP handle, served by a direct downloader. No MVP source
	// uses it; it exists so the routing switch is total.
	Direct Kind = "direct"
)

// Release is the thing being acquired: a specific edition of a work.
//
// It is deliberately not a metadata.Book. A Book is the work; a Release is one
// published edition of it, with its own identifier, narrator and publisher.
// The distinction is what lets the releases panel offer a choice, and what
// lets a source search for the edition the user actually picked.
type Release struct {
	// Title is the clean title of the work.
	Title string
	// Author is "First Last".
	Author string
	// Series is the bare series name, empty for a standalone.
	Series string
	// SeriesSequence is the position within Series.
	SeriesSequence string
	// Narrator is the credited narrator, or "GraphicAudio".
	Narrator string
	// Publisher is the publishing house.
	Publisher string
	// Unabridged is what the catalogue said about the edition. Scoring
	// treats a contradiction with the share text ("abridged" in a path for
	// an unabridged release) as strong evidence of the wrong edition.
	Unabridged bool
	// PartCount is how many files the release is published in: 1 for a
	// single-file narration, N for a multi-part dramatisation. Zero means
	// unknown and is treated as 1.
	PartCount int
	// Identifier is this edition's catalogue key. Empty when the release came
	// from a free-text query that matched no catalogue.
	Identifier metadata.Identifier
	// Query is the original free-text query, kept so a source can fall back
	// to it when the structured fields are empty.
	Query string
}

// Candidate is one obtainable copy of a release, as a source found it.
type Candidate struct {
	// Source is the Name of the source that produced this candidate.
	Source string
	// Title is the release title as the source presents it, which is often
	// messier than the catalogue title.
	Title string
	// Format is the container, lowercase: "m4b", "mp3", "flac".
	Format string
	// Bitrate is the audio bitrate in kbps. Zero when unknown.
	Bitrate int
	// SizeBytes is the total size. Zero when unknown.
	SizeBytes int64
	// Part is which part of a multi-part release this is, and Parts is how
	// many there are. Part 1 of 1 means a complete single-file release.
	Part  int
	Parts int
	// Seeders is how many peers are offering it. Zero for a p2p source that
	// reports availability differently.
	Seeders int
	// Files is the concrete file list behind the offer, named on the peer's
	// share. It is what the confirm screen presents for per-file selection,
	// and what enqueue replays.
	Files []File
	// RemoteDir is the peer-side directory the files live in, when the
	// source recorded one separately from the filenames.
	RemoteDir string
	// ExternalURL links out to the offer's own page (e.g. the ABB listing).
	// Empty when the source has no per-offer page. Display only: the
	// confirm card renders it as a "view listing" link so the user can
	// read what the scraper could not extract.
	ExternalURL string
	// Handle is what the Grabber needs to start the transfer.
	Handle Handle
}

// File is one file inside an offer.
type File struct {
	// Name is the full source-side path.
	Name string
	// Size is the byte size. Zero when the source did not report it.
	Size int64
}

// Handle is an opaque reference to a transferable copy.
//
// Its contents are the source's business: a torrent handle carries a magnet
// URI, a p2p handle carries a peer and a file list. The Grabber that receives
// it is chosen by the source's Kind, so the two always agree on the shape.
type Handle struct {
	// Kind is the transport, copied from the source so the Grabber can
	// reject a handle it cannot serve.
	Kind Kind
	// Ref is the source-specific payload.
	Ref any
}
