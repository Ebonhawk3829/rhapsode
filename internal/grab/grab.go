// Package grab defines the downloader side of rhapsode: how a transfer is
// started and how its progress is observed.
//
// A Grabber tracks bytes. It does not search, and it does not decide what to
// download — it is handed a handle that a Source already chose, and it reports
// what happened to it.
package grab

import (
	"context"
	"time"

	"github.com/Ebonhawk3829/rhapsode/internal/source"
)

// Grabber is one downloader rhapsode can drive.
//
// Implementations must be safe for concurrent use: several jobs may be in
// flight at once, and the progress poller reads every one of them on a timer.
type Grabber interface {
	// Name identifies the downloader in logs and in the UI, e.g.
	// "transmission".
	Name() string

	// Kind is the transport this downloader serves. A Grabber is only ever
	// handed handles of its own Kind.
	Kind() source.Kind

	// Add starts a transfer and returns the downloader's own identifier for
	// it. The identifier is opaque and is what Status is later called with.
	Add(ctx context.Context, h source.Handle) (ID, error)

	// Status reports the current state of a transfer.
	//
	// A transfer the downloader no longer knows about is not an error: it
	// returns StateUnknown, which the job model treats as a failure to
	// reconcile rather than a transport problem.
	Status(ctx context.Context, id ID) (State, error)

	// Located reports where a completed transfer's files are, as rhapsode
	// sees the filesystem.
	//
	// The paths a downloader writes under its own configuration are not the
	// paths rhapsode reads: the two may mount the same host directory at
	// different names. Located does the translation, so verification never
	// has to guess a path from a slug — the design contract is that staged
	// files are located through the transport's own API, never by path
	// arithmetic on the destination slug.
	//
	// It is only meaningful for a transfer that has reached Done; callers
	// check Status first. A transfer the downloader no longer knows about
	// returns an error rather than a guess.
	Located(ctx context.Context, id ID) ([]string, error)
}

// ID is a downloader's own identifier for a transfer.
type ID string

// State is what a downloader reports about a transfer.
type State struct {
	// Phase is the coarse lifecycle position.
	Phase Phase
	// Progress is completion from 0 to 1.
	Progress float64
	// BytesDone and BytesTotal are the raw counters behind Progress. Total is
	// zero when the downloader has not yet learned the size.
	BytesDone  int64
	BytesTotal int64
	// Rate is the current transfer rate in bytes per second.
	Rate int64
	// ETA is the downloader's own estimate of time remaining. Zero when it
	// has none.
	ETA time.Duration
	// Path is where the downloader is writing, as the downloader sees it.
	// Empty until the transfer has started.
	Path string
	// Message carries the downloader's own error text for a failed or
	// stalled transfer. Empty otherwise.
	Message string
}

// Phase is the coarse lifecycle position of a transfer.
type Phase string

const (
	// Queued means accepted but not started.
	Queued Phase = "queued"
	// Active means transferring.
	Active Phase = "active"
	// Done means complete and verified by the downloader.
	Done Phase = "done"
	// Failed means the downloader gave up.
	Failed Phase = "failed"
	// Stalled means no progress and no peers, but not yet abandoned.
	Stalled Phase = "stalled"
	// Unknown means the downloader does not recognise the identifier.
	Unknown Phase = "unknown"
)

// Terminal reports whether a phase is an end state that will not change
// without intervention.
func (p Phase) Terminal() bool {
	return p == Done || p == Failed
}
