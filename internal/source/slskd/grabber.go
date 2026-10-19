package slskd

import (
	"context"
	"errors"
	"net/http"
	"path"
	"strings"

	"github.com/Ebonhawk3829/rhapsode/internal/grab"
	"github.com/Ebonhawk3829/rhapsode/internal/source"
)

// Grabber tracks slskd transfers.
type Grabber struct {
	client *Client
	// root is slskd's download root, as slskd sees it. Destinations are
	// relative to it, so it is the prefix that must be translated.
	root string
	// localRoot is the same host directory as rhapsode sees it.
	localRoot string
}

// NewGrabber builds a Grabber over an existing client. root is slskd's
// download root as slskd sees it (e.g. /downloads/slskd-complete);
// localRoot is the same host directory as rhapsode sees it.
func NewGrabber(c *Client, root, localRoot string) *Grabber {
	return &Grabber{client: c, root: root, localRoot: localRoot}
}

// Name identifies the downloader.
func (g *Grabber) Name() string { return "slskd" }

// Kind is the transport this grabber serves.
func (g *Grabber) Kind() source.Kind { return source.P2P }

// Add is a no-op for slskd.
//
// slskd enqueues at search time: Source.Grab already handed the files to the
// batch endpoint, and the transfer exists before Add is called. Add therefore
// only validates the handle and returns the identifier Status will use.
//
// The identifier is the batch ID, not the peer: a peer may serve several jobs
// at once, and a batch is exactly the set of files one job enqueued. Keying
// on the peer would make one job's Done depend on another job's files.
func (g *Grabber) Add(_ context.Context, h source.Handle) (grab.ID, error) {
	ref, ok := h.Ref.(grabRef)
	if !ok {
		return "", errors.New("slskd: handle is not a slskd grab reference")
	}
	if ref.BatchID == "" {
		return "", errors.New("slskd: handle has no batch id")
	}
	return grab.ID(ref.BatchID), nil
}

// Status reports the state of a transfer.
//
// The state is aggregated across the batch's files: the transfer is done
// only when every file is done, and failed as soon as any file has failed.
// A batch slskd no longer knows is Unknown, not an error — retention
// eventually expires completed batches, and a job resumed after that must
// reconcile rather than crash.
func (g *Grabber) Status(ctx context.Context, id grab.ID) (grab.State, error) {
	b, err := g.load(ctx, id)
	if err != nil {
		return grab.State{}, err
	}
	if b == nil {
		return grab.State{Phase: grab.Unknown}, nil
	}
	return aggregate(b.Transfers), nil
}

// Located reports the completed files' paths as rhapsode sees them.
//
// slskd moves a finished file to <download root>/<batch destination>/<file
// name>, where the file name is the remote file's base name. The batch
// record carries the destination it was enqueued with, so the location is
// read back from slskd rather than predicted: the design contract is that
// staged files are located through the transport's API, never by path
// arithmetic on the slug.
//
// The download root as slskd sees it and as rhapsode sees it can differ —
// the two containers may mount the same host directory at different names —
// so the grabber was built with both and translates the prefix.
func (g *Grabber) Located(ctx context.Context, id grab.ID) ([]string, error) {
	b, err := g.load(ctx, id)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return nil, errors.New("slskd: batch not found; cannot locate files")
	}
	if b.Options.Destination == "" {
		return nil, errors.New("slskd: batch has no destination recorded")
	}

	dir := g.localPath(b.Options.Destination)
	var out []string
	for _, t := range b.Transfers {
		if classify(t.State) != grab.Done {
			continue
		}
		// The remote filename carries the peer's own separators, which on a
		// Windows peer are backslashes; path.Base reads only the local
		// separator, so normalise first.
		name := path.Base(strings.ReplaceAll(t.Filename, "\\", "/"))
		out = append(out, path.Join(dir, name))
	}
	if len(out) == 0 {
		return nil, errors.New("slskd: batch has no completed transfers")
	}
	return out, nil
}

// load fetches one batch, reporting a batch slskd does not know as nil
// rather than an error: Unknown is a state the caller reconciles, while a
// transport failure is an error the caller retries.
func (g *Grabber) load(ctx context.Context, id grab.ID) (*batch, error) {	b, err := g.client.batchByID(ctx, string(id))
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			return nil, nil
		}
		return nil, err
	}
	return b, nil
}

// localPath translates a slskd-side path under its download root into the
// same file as rhapsode sees it.
//
// slskd's destination is always relative to its download root, so the root
// prefix is the only thing that can differ between the two views.
func (g *Grabber) localPath(p string) string {
	if g.root != "" && strings.HasPrefix(p, g.root) {
		return g.localRoot + strings.TrimPrefix(p, g.root)
	}
	return path.Join(g.localRoot, p)
}

// Cancel stops a batch's transfers and removes them with their partial
// files. An unknown batch is a no-op: the bytes are already gone.
func (g *Grabber) Cancel(ctx context.Context, id grab.ID) error {
	b, err := g.load(ctx, id)
	if err != nil {
		return err
	}
	if b == nil {
		return nil
	}
	for _, t := range b.Transfers {
		if err := g.client.cancelTransfer(ctx, b.Username, t.ID, true); err != nil {
			var apiErr *APIError
			if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
				continue
			}
			return err
		}
	}
	return nil
}

// aggregate folds a peer's transfer records into one state.
func aggregate(transfers []transfer) grab.State {
	var (
		done      int
		failed    int
		active    int
		queued    int
		bytesDone int64
		bytesTot  int64
		rate      int64
		message   string
		path      string
	)

	for _, t := range transfers {
		bytesDone += t.BytesTransferred
		bytesTot += t.Size
		rate += int64(t.AverageSpeed)
		if path == "" {
			path = t.Filename
		}

		switch classify(t.State) {
		case grab.Done:
			done++
		case grab.Failed:
			failed++
			if message == "" {
				message = t.State
			}
		case grab.Active:
			active++
		default:
			queued++
		}
	}

	st := grab.State{
		BytesDone:  bytesDone,
		BytesTotal: bytesTot,
		Rate:       rate,
		Path:       path,
		Message:    message,
	}
	if bytesTot > 0 {
		st.Progress = float64(bytesDone) / float64(bytesTot)
	}

	switch {
	case failed > 0:
		st.Phase = grab.Failed
	case done == len(transfers):
		st.Phase = grab.Done
		st.Progress = 1
	case active > 0:
		st.Phase = grab.Active
	case queued > 0:
		st.Phase = grab.Queued
	default:
		st.Phase = grab.Unknown
	}
	return st
}

// classify maps slskd's comma-joined state flags onto a phase.
//
// slskd reports state as a set of flags, e.g. "Completed, Succeeded" or
// "Queued, Remotely". Failure is checked first because a record can carry both
// a terminal flag and an error flag — "Completed, Failed" is a failure, not a
// success, and reading the flags left to right would report the opposite.
func classify(state string) grab.Phase {
	s := strings.ToLower(state)

	switch {
	case strings.Contains(s, "failed"), strings.Contains(s, "errored"),
		strings.Contains(s, "cancelled"), strings.Contains(s, "rejected"),
		strings.Contains(s, "timedout"), strings.Contains(s, "timed out"):
		return grab.Failed
	case strings.Contains(s, "succeeded"):
		return grab.Done
	case strings.Contains(s, "inprogress"), strings.Contains(s, "in progress"),
		strings.Contains(s, "initializing"), strings.Contains(s, "transferring"):
		return grab.Active
	case strings.Contains(s, "queued"), strings.Contains(s, "requested"):
		return grab.Queued
	default:
		return grab.Unknown
	}
}

// compile-time check that the grabber satisfies the interface.
var _ grab.Grabber = (*Grabber)(nil)
