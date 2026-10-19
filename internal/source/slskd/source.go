package slskd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/Ebonhawk3829/rhapsode/internal/source"
	"github.com/Ebonhawk3829/rhapsode/internal/store"
)

// searchTimeout is how long slskd waits for peer responses. Soulseek searches
// are slow by nature; a short timeout returns a thin result set.
const searchTimeout = 15 * time.Second

// pollInterval is how often the search is checked for completion.
const pollInterval = 500 * time.Millisecond

// Source searches the Soulseek network through slskd.
type Source struct {
	client *Client
}

// NewSource builds a Source over an existing client.
func NewSource(c *Client) *Source { return &Source{client: c} }

// Name identifies the source.
func (s *Source) Name() string { return "slskd" }

// Kind is the transport slskd handles use.
func (s *Source) Kind() source.Kind { return source.P2P }

// Search finds candidate copies of a release.
//
// The query is built from whatever the release carries. A release with no
// metadata at all — a free-text query that matched no catalogue — still
// searches, using the raw query text.
func (s *Source) Search(ctx context.Context, rel source.Release) ([]source.Candidate, error) {
	query := searchQuery(rel)
	if query == "" {
		return nil, errors.New("slskd: nothing to search for")
	}

	id, err := s.client.startSearch(ctx, query, int(searchTimeout.Milliseconds()))
	if err != nil {
		return nil, err
	}
	// The search is scratch state in slskd; leaving it behind accumulates.
	defer func() { _ = s.client.deleteSearch(context.WithoutCancel(ctx), id) }()

	if err := s.wait(ctx, id); err != nil {
		return nil, err
	}

	responses, err := s.client.searchResponses(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.candidates(responses), nil
}

// wait polls until the search completes or the context ends.
func (s *Source) wait(ctx context.Context, id string) error {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			st, err := s.client.searchState(ctx, id)
			if err != nil {
				return err
			}
			if st.IsComplete {
				return nil
			}
		}
	}
}

// candidates flattens peer responses into one candidate per peer.
//
// A peer offering several files is one candidate, not several: the files are
// parts of the same release, and grabbing half of them is not a partial
// download but a broken one. The candidate carries the whole file list in its
// handle.
func (s *Source) candidates(responses []userResponse) []source.Candidate {
	var out []source.Candidate
	for _, r := range responses {
		files := append(append([]file{}, r.Files...), r.LockedFiles...)
		if len(files) == 0 {
			continue
		}

		c := source.Candidate{
			Source:    s.Name(),
			Title:     releaseTitle(files),
			Format:    dominantFormat(files),
			Bitrate:   dominantBitrate(files),
			SizeBytes: totalSize(files),
			Part:      1,
			Parts:     1,
			Seeders:   availability(r),
			Handle: source.Handle{
				Kind: source.P2P,
				Ref: p2pRef{
					Username: r.Username,
					Files:    files,
				},
			},
		}
		c.Files = toSourceFiles(files)
		c.RemoteDir = commonDir(files)
		out = append(out, c)
	}

	// Best first: a free upload slot and a short queue beat raw speed, because
	// a fast peer that will not serve you is worth nothing.
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Seeders > out[j].Seeders
	})
	return out
}

// toSourceFiles converts internal file records to the source package shape.
func toSourceFiles(files []file) []source.File {
	out := make([]source.File, 0, len(files))
	for _, f := range files {
		out = append(out, source.File{Name: f.Filename, Size: f.Size})
	}
	return out
}

// commonDir is the deepest directory every file shares, which is what names
// the share on the confirm screen and feeds the scorer's directory check.
//
// A single-file offer has no meaningful shared directory beyond its parent,
// which is still used: parents carry edition markers surprisingly often.
func commonDir(files []file) string {
	if len(files) == 0 {
		return ""
	}
	dir := path.Dir(files[0].Filename)
	for _, f := range files[1:] {
		for dir != "" && !strings.HasPrefix(f.Filename, dir+string(os.PathSeparator)) {
			next := path.Dir(dir)
			if next == dir {
				return ""
			}
			dir = next
		}
	}
	return dir
}

// Grab enqueues a candidate's files into the job's staging destination.
//
// The destination is audiobooks/<slug>, relative to slskd's download root.
// slskd creates it; rhapsode does not need the directory to exist. The slug
// arrives explicitly rather than derived from the listing title: the title
// is peer text, the slug is the job's identity, and the orphan cleanup
// sweeps by slug.
//
// The candidate may arrive from two origins: a live search response (ref
// holds the peer and files directly) or a confirmed selection rebuilt from
// persisted candidates (files and username come from the candidate's own
// fields). Both must enqueue identically.
func (s *Source) Grab(ctx context.Context, c source.Candidate, slug string) (source.Handle, error) {
	username, peerFiles := refPeer(c)
	if username == "" || len(peerFiles) == 0 {
		return source.Handle{}, errors.New("slskd: handle has no peer or files")
	}

	destination := destinationFor(slug)
	files := make([]batchFile, 0, len(peerFiles))
	for _, f := range peerFiles {
		files = append(files, batchFile{Filename: f.Filename, Size: f.Size})
	}

	resp, err := s.client.enqueueBatch(ctx, username, destination, files)
	if err != nil {
		return source.Handle{}, err
	}
	if len(resp.Failures) > 0 {
		return source.Handle{}, fmt.Errorf("slsdk: %d of %d files rejected: %s",
			len(resp.Failures), len(files), resp.Failures[0].Message)
	}

	return source.Handle{
		Kind: source.P2P,
		Ref: grabRef{
			Username: username,
			BatchID:  resp.Batch.ID,
			Files:    peerFiles,
		},
	}, nil
}

// refPeer extracts the peer and file list from a candidate.
//
// A live search response carries both inside its handle ref. A confirmed
// selection rebuilt from persistence carries the username on the ref-less
// path: the runner sets it from the stored candidate. Either way the result
// is the same shape for enqueue.
func refPeer(c source.Candidate) (string, []file) {
	if ref, ok := c.Handle.Ref.(p2pRef); ok {
		return ref.Username, ref.Files
	}
	// Rebuilt candidate: the user's selection arrived as plain files. The
	// peer rides the ref when the caller rebuilt it through Rebuild.
	if ref, ok := c.Handle.Ref.(rebuiltRef); ok {
		var files []file
		for _, f := range c.Files {
			files = append(files, file{Filename: f.Name, Size: f.Size})
		}
		return ref.Username, files
	}
	return "", nil
}

// rebuiltRef is the handle of a candidate rebuilt from persistence: it names
// the peer and nothing else, because the files travelled on the candidate.
type rebuiltRef struct {
	Username string
}

// p2pRef is the source-side handle: what to enqueue and from whom.
type p2pRef struct {
	Username string
	Files    []file
}

// Peer exposes the username to the runner's candidate mapper.
func (r p2pRef) Peer() string { return r.Username }

// Rebuild produces a valid Grab handle for a candidate restored from the
// store.
//
// The runner knows the username from the record; only this package knows the
// handle shape, so reconstruction belongs here.
func (s *Source) Rebuild(username string, _ []store.CandidateFile) source.Handle {
	return source.Handle{
		Kind: source.P2P,
		Ref:  rebuiltRef{Username: username},
	}
}

// grabRef is the grabber-side handle: what was enqueued and how to find it.
type grabRef struct {
	Username string
	BatchID  string
	Files    []file
}

// destinationFor is the staging subdirectory for a job slug. slskd treats
// the value as a path relative to its download root, so the slug is cleaned
// to prevent traversal.
func destinationFor(slug string) string {
	clean := slugify(slug)
	if clean == "" {
		clean = "unknown"
	}
	return path.Join("audiobooks", clean)
}

// searchQuery builds the Soulseek query from a release.
//
// Soulseek matches on filename text, so the query is the author and title
// rather than the series: a peer's files are named after the book, not its
// position in a series.
func searchQuery(rel source.Release) string {
	parts := make([]string, 0, 2)
	if rel.Author != "" {
		parts = append(parts, rel.Author)
	}
	if rel.Title != "" {
		parts = append(parts, rel.Title)
	}
	if len(parts) == 0 {
		return strings.TrimSpace(rel.Query)
	}
	return strings.Join(parts, " ")
}

// releaseTitle names a candidate from its files.
func releaseTitle(files []file) string {
	if len(files) == 0 {
		return ""
	}
	// The parent directory is usually the release name; the filename is the
	// track. Prefer the directory when it is more than a generic container.
	dir := path.Dir(files[0].Filename)
	base := path.Base(dir)
	if base != "" && base != "." && base != "/" {
		return base
	}
	return strings.TrimSuffix(path.Base(files[0].Filename), path.Ext(files[0].Filename))
}

// dominantFormat is the most common extension among the files.
func dominantFormat(files []file) string {
	counts := map[string]int{}
	for _, f := range files {
		ext := strings.TrimPrefix(strings.ToLower(f.Extension), ".")
		if ext == "" {
			ext = strings.TrimPrefix(strings.ToLower(path.Ext(f.Filename)), ".")
		}
		if ext != "" {
			counts[ext]++
		}
	}
	return mostCommon(counts)
}

// dominantBitrate is the highest bitrate among the files, which is the one
// that describes the release when a peer mixes qualities.
func dominantBitrate(files []file) int {
	best := 0
	for _, f := range files {
		if f.BitRate != nil && *f.BitRate > best {
			best = *f.BitRate
		}
	}
	return best
}

func totalSize(files []file) int64 {
	var n int64
	for _, f := range files {
		n += f.Size
	}
	return n
}

// freeSlotBonus is the availability offset for a peer with a free upload
// slot. The runner records FreeSlot for candidates at or above this score;
// the constant lives here (the scoring is this package's business) and the
// threshold is restated at the use site with a pointer back here, so the two
// cannot drift silently.
const freeSlotBonus = 1000

// availability scores a peer. A free upload slot is worth more than speed: a
// peer with no slot will queue the request indefinitely.
func availability(r userResponse) int {
	score := 0
	if r.HasFreeUploadSlot {
		score += freeSlotBonus
	}
	// A shorter queue is better, so invert it into the score.
	score += max(0, 100-r.QueueLength)
	return score
}

func mostCommon(counts map[string]int) string {
	best, bestN := "", 0
	for k, n := range counts {
		if n > bestN || (n == bestN && k < best) {
			best, bestN = k, n
		}
	}
	return best
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

// compile-time check that the source satisfies the interface.
var _ source.Source = (*Source)(nil)
