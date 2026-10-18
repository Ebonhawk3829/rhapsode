// Package job runs acquisitions.
//
// A job is one attempt to acquire a release: search the sources for a copy,
// rank the candidates, grab the best, and watch it land in staging. The Job
// type and its persistence live in store; this package is the machinery that
// moves a job through its lifecycle.
//
// The runner is deliberately single-threaded per job and serial across jobs —
// Soulseek uploads are the scarce resource and a queue that hammers peers
// from several jobs at once is its own ban risk. Concurrency, when it comes,
// should be a bounded worker pool above this, not free-for-all goroutines.
package job

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/Ebonhawk3829/rhapsode/internal/grab"
	"github.com/Ebonhawk3829/rhapsode/internal/merge"
	"github.com/Ebonhawk3829/rhapsode/internal/metadata"
	"github.com/Ebonhawk3829/rhapsode/internal/notify"
	"github.com/Ebonhawk3829/rhapsode/internal/request"
	"github.com/Ebonhawk3829/rhapsode/internal/score"
	"github.com/Ebonhawk3829/rhapsode/internal/shelve"
	"github.com/Ebonhawk3829/rhapsode/internal/source"
	"github.com/Ebonhawk3829/rhapsode/internal/store"
	"github.com/Ebonhawk3829/rhapsode/internal/tag"
	"github.com/Ebonhawk3829/rhapsode/internal/verify"
)

// Runner drives jobs from Pending to a terminal state.
type Runner struct {
	st       *store.Store
	sources  []source.Source
	grabbers map[source.Kind]grab.Grabber
	// requests coins slugs and validates picks. Separated from the store so
	// the slug algorithm has exactly one owner.
	requests *request.Creator
	// verifier inspects the staged files' tags against the release once the
	// download lands. Optional in tests that stop at downloading; a nil
	// verifier leaves verifying jobs parked (never silently passed).
	verifier *verify.Verifier
	// merger combines a verified multi-file batch into the one .m4b the
	// library contract requires.
	merger *merge.Merger
	// tagger writes the library metadata contract onto the merged file.
	tagger *tag.Tagger
	// shelver copies the tagged file into the library and scans ABS.
	shelver *shelve.Shelver
	// ownedChecker asks the library whether a title is already shelved.
	// Optional: nil disables the duplicate guard.
	ownedChecker ownedChecker
	// notifier pushes the two moments the user cares about: done and
	// failed. Nil-safe — an unconfigured deployment runs without pushes.
	notifier *notify.Notifier
	// interval is how often the runner sweeps for work.
	interval time.Duration
}

// New builds a Runner.
//
// Grabbers are routed by transport: the runner picks the grabber a source's
// Kind names, the same way a reverse proxy picks its upstream. A Kind with no
// grabber means sources of that kind fail their jobs with a clear error
// rather than silently disappearing.
func New(st *store.Store, sources []source.Source, grabbers map[source.Kind]grab.Grabber) *Runner {
	r := &Runner{
		st:       st,
		sources:  sources,
		grabbers: grabbers,
		requests: request.New(st),
		interval: 10 * time.Second,
	}
	r.SetVerifier(verify.New(verify.ToneDumper(context.Background())))
	return r
}

// SetVerifier replaces the verifier. Production wires the tone-backed one;
// tests inject scripted dumps.
func (r *Runner) SetVerifier(v *verify.Verifier) {
	r.verifier = v
}

// SetMerger replaces the merger (tests script m4b-tool invocations).
func (r *Runner) SetMerger(m *merge.Merger) {
	r.merger = m
}

// SetTagger replaces the tagger (tests script tone invocations).
func (r *Runner) SetTagger(t *tag.Tagger) {
	r.tagger = t
}

// SetShelver replaces the shelver (tests script the library copy and the
// ABS calls).
func (r *Runner) SetShelver(s *shelve.Shelver) {
	r.shelver = s
}

// SetNotifier replaces the notifier (nil disables pushes).
func (r *Runner) SetNotifier(n *notify.Notifier) {
	r.notifier = n
}

// ownedChecker is the duplicate guard the search step consults. The shelver
// implements it against ABS; tests script it.
type ownedChecker interface {
	AlreadyOwned(ctx context.Context, title string) (bool, error)
}

// SetOwnedChecker wires the duplicate guard (nil disables it).
func (r *Runner) SetOwnedChecker(o ownedChecker) {
	r.ownedChecker = o
}

// Request implements web.RequestCreator: a release pick becomes a durable
// job the sweep loop will pick up on its next pass.
func (r *Runner) Request(rel request.Release) (*store.Job, error) {
	return r.requests.Request(rel)
}

// Run sweeps until the context ends.
func (r *Runner) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.sweep(ctx); err != nil {
				log.Printf("job: sweep: %v", err)
			}
		}
	}
}

// sweep advances every job that needs advancing.
func (r *Runner) sweep(ctx context.Context) error {
	jobs, err := r.st.List("")
	if err != nil {
		return err
	}
	for _, j := range jobs {
		if j.State.Terminal() {
			continue
		}
		if err := r.advance(ctx, j); err != nil {
			log.Printf("job: %s (%s): %v", j.ID[:8], j.Title, err)
		}
	}
	return nil
}

// advance moves one job one lifecycle step.
func (r *Runner) advance(ctx context.Context, j *store.Job) error {
	switch j.State {
	case store.Pending:
		return r.search(ctx, j)
	case store.Searching:
		return r.search(ctx, j)
	case store.Downloading:
		return r.poll(ctx, j)
	case store.Verifying:
		return r.verifyStaged(ctx, j)
	case store.Merging:
		return r.mergeStaged(ctx, j)
	case store.Tagging:
		return r.tagStaged(ctx, j)
	case store.Shelving:
		return r.shelveStaged(ctx, j)
	default:
		// Confirm waits for the user.
		return nil
	}
}

// search finds offers for the job's release, records them, and stops.
//
// Searching never grabs. The confirm screen decides what to download: the
// scorer orders the offers, but which files to take is the user's judgement
// over the peer's share, and the machine has no opinion worth auto-grabbing
// around.
//
// A pass that finds nothing is the one objective exit: there is nothing to
// show and nothing to choose, so the job fails there. A retry is a new
// request, not a poller.
func (r *Runner) search(ctx context.Context, j *store.Job) error {
	rel := releaseOf(j)

	// The duplicate guard runs before any source is asked: downloading a
	// book the library already carries wastes the user's confirm click and
	// a peer's bandwidth. An unreachable ABS must not block the search —
	// the guard degrades to disabled, the shelving step re-checks nothing
	// (double-shelving is a visible, deletable nuisance, not corruption).
	if r.ownedChecker != nil {
		owned, err := r.ownedChecker.AlreadyOwned(ctx, j.Title)
		if err != nil {
			_ = r.st.AddEvent(j.ID, "search", fmt.Sprintf("ownership check skipped: %v", err))
		} else if owned {
			return r.fail(j, "the library already carries this title")
		}
	}

	j.State = store.Searching
	if err := r.st.Update(j); err != nil {
		return err
	}
	_ = r.st.AddEvent(j.ID, "search", fmt.Sprintf("searching %d sources for %q", len(r.sources), rel.Title))

	nFound := 0
	for _, s := range r.sources {
		cands, err := s.Search(ctx, rel)
		if err != nil {
			// One dead source must not fail the job: the others may still
			// carry a copy.
			_ = r.st.AddEvent(j.ID, "search", fmt.Sprintf("source %s failed: %v", s.Name(), err))
			continue
		}
		for _, c := range cands {
			res := score.Grade(rel, c)
			sc := &store.Candidate{
				JobID:     j.ID,
				Source:    c.Source,
				Kind:      string(c.Handle.Kind),
				Username:  candidateUser(c),
				Title:     c.Title,
				RemoteDir: c.RemoteDir,
				Files:     filesOf(c),
				SizeBytes: c.SizeBytes,
				Format:    c.Format,
				Bitrate:   c.Bitrate,
				FreeSlot:  c.Seeders >= 1000,
				Score:     res.Score,
				Band:      res.Band,
				Reasons:   res.Reasons,
			}
			if err := r.st.SaveCandidate(sc); err != nil {
				return err
			}
			nFound++
		}
	}
	if nFound == 0 {
		return r.fail(j, "no candidates from any source")
	}

	// Stop and ask. The user's options live on the confirm screen; the
	// runner does not revisit jobs in this state on its own — a job in
	// confirm stays there until Confirm, Reject or Cancel moves it.
	j.State = store.Confirm
	if err := r.st.Update(j); err != nil {
		return err
	}
	if err := r.st.AddEvent(j.ID, "search", fmt.Sprintf("%d offers found; awaiting file selection", nFound)); err != nil {
		return err
	}
	// The confirm stop is one of the moments the user cares about: the
	// machine cannot proceed without their pick, so it says so.
	r.notify(ctx, "Offers ready", j.Title+": "+fmt.Sprintf("%d offers found — pick one to start the download", nFound))
	return nil
}

// Confirm hands the user's file selection to the download pipeline.
//
// It is the resume action for a job in confirm: the chosen candidate is
// marked, the selection enqueue happens through the candidate's recorded
// files, and the job proceeds to downloading. An empty selection means
// "take everything the offer showed", which is the usual case.
func (r *Runner) Confirm(ctx context.Context, jobID, candidateID string, selected []store.CandidateFile) (*store.Job, error) {
	j, err := r.st.Get(jobID)
	if err != nil {
		return nil, err
	}
	if j.State != store.Confirm {
		return nil, fmt.Errorf("job %s is in state %q, not confirm", j.ID[:8], j.State)
	}

	c, err := r.st.CandidateByID(jobID, candidateID)
	if err != nil {
		return nil, err
	}

	// Resolve the selection to the recorded file set. Everything offered is
	// the default because that is what "I want this release" usually means;
	// a subset is the user trimming deliberately.
	files := c.Files
	if len(selected) > 0 {
		files = selected
	}
	if len(files) == 0 {
		return nil, errors.New("candidate has no files to download")
	}

	if err := r.st.ConfirmCandidate(jobID, candidateID, files); err != nil {
		return nil, err
	}

	// The transport a candidate travels on is persisted on the row, so the
	// routing contract stays checkable before any handle is rebuilt.
	g, ok := r.grabbers[source.Kind(c.Kind)]
	if !ok {
		return nil, fmt.Errorf("no grabber for transport %q", c.Kind)
	}

	// Find the origin source first: it both reconstructs the handle and
	// enqueues the selection.
	var origin source.Source
	for _, s := range r.sources {
		if s.Name() == c.Source {
			origin = s
			break
		}
	}
	if origin == nil {
		return nil, fmt.Errorf("source %q is not configured", c.Source)
	}
	// Rebuild the candidate as a source.Candidate so its own origin source
	// can enqueue it.
	sc, err := toSourceCandidate(c, files, origin)
	if err != nil {
		return nil, err
	}

	grabbable, err := origin.Grab(ctx, sc)
	if err != nil {
		return nil, fmt.Errorf("grab failed: %w", err)
	}
	h, err := g.Add(ctx, grabbable)
	if err != nil {
		return nil, fmt.Errorf("grab failed: %w", err)
	}

	j.State = store.Downloading
	j.Source = c.Source
	j.Grabber = g.Name()
	j.GrabID = string(h)
	j.ConfirmedCandidateID = candidateID
	j.Phase = string(grab.Queued)
	if err := r.st.Update(j); err != nil {
		return nil, err
	}
	if err := r.st.AddEvent(j.ID, "grab", fmt.Sprintf(
		"confirmed %d files from %s (%s): %s",
		len(files), c.Source, c.Username, renderSize(c.SizeBytes))); err != nil {
		return nil, err
	}
	return r.st.Get(jobID)
}

// RejectCandidate records the user's rejection of one offer.
//
// The job stays in confirm as long as anything remains to choose from; only
// when the last offer is rejected does the job fail, because a confirm state
// with an empty list is a screen with no buttons.
func (r *Runner) RejectCandidate(jobID, candidateID string) (*store.Job, error) {
	j, err := r.st.Get(jobID)
	if err != nil {
		return nil, err
	}
	if j.State != store.Confirm {
		return nil, fmt.Errorf("job %s is in state %q, not confirm", j.ID[:8], j.State)
	}
	if err := r.st.RejectCandidate(jobID, candidateID); err != nil {
		return nil, err
	}
	left, err := r.st.CandidateCount(jobID)
	if err != nil {
		return nil, err
	}
	if left == 0 {
		return nil, r.fail(j, "all candidates rejected")
	}
	_ = r.st.AddEvent(j.ID, "reject", fmt.Sprintf("candidate %s rejected; %d remain", candidateID[:8], left))
	return j, nil
}

// Cancel stops a job in confirm (or terminal-adjacent states) at the user's
// word. It is a decision, not an error: the audit records it as such.
func (r *Runner) Cancel(jobID string) (*store.Job, error) {
	j, err := r.st.Get(jobID)
	if err != nil {
		return nil, err
	}
	if j.State.Terminal() {
		return j, nil
	}
	j.State = store.Cancelled
	if err := r.st.Update(j); err != nil {
		return nil, err
	}
	_ = r.st.AddEvent(j.ID, "cancel", "cancelled by user")
	return j, nil
}

// poll checks the downloader and hands a completed transfer to verify.
//
// Completing the download means staging is complete — not that the copy is
// the release it claimed. The verify phase owns that judgement.
func (r *Runner) poll(ctx context.Context, j *store.Job) error {
	g, ok := r.grabbers[r.grabKindOf(j)]
	if !ok {
		return r.fail(j, fmt.Sprintf("grabber %q vanished mid-job", j.Grabber))
	}

	st, err := g.Status(ctx, grab.ID(j.GrabID))
	if err != nil {
		return fmt.Errorf("status: %w", err)
	}

	j.Phase = string(st.Phase)
	j.Progress = st.Progress
	if err := r.st.Update(j); err != nil {
		return err
	}

	switch st.Phase {
	case grab.Done:
		j.State = store.Verifying
		if err := r.st.Update(j); err != nil {
			return err
		}
		return r.st.AddEvent(j.ID, "download", "staged in "+j.Slug+"; verifying")
	case grab.Failed:
		return r.fail(j, "download failed: "+st.Message)
	}
	return nil
}

// verifyStaged locates the completed files through the transport's API and
// inspects their tags against the release.
//
// A failed verification parks the job back in confirm with the offending
// candidate rejected and the reason recorded (docs/design.md): the user
// re-engages on the confirm screen instead of re-running a search, so a
// wrong grab costs one download, not the whole request. A nil verifier
// leaves the job parked in verifying — never silently passed.
func (r *Runner) verifyStaged(ctx context.Context, j *store.Job) error {
	if r.verifier == nil {
		return nil
	}
	g, ok := r.grabbers[r.grabKindOf(j)]
	if !ok {
		return r.fail(j, fmt.Sprintf("grabber %q vanished mid-job", j.Grabber))
	}
	paths, err := g.Located(ctx, grab.ID(j.GrabID))
	if err != nil {
		return fmt.Errorf("locate staged files: %w", err)
	}

	report, err := r.verifier.Inspect(paths, verifyReleaseOf(j))
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}

	if !report.OK {
		reason := "verification failed: " + firstNegative(report.Reasons)
		if err := r.st.AddEvent(j.ID, "verify", reason+"; "+fmt.Sprintf("%d files inspected", len(paths))); err != nil {
			return err
		}
		return r.parkBackInConfirm(j, reason)
	}

	_ = r.st.AddEvent(j.ID, "verify", fmt.Sprintf(
		"verified %d files (%s audio, %d chapters)",
		len(paths), verify.DurationString(report.DurationMS), report.Chapters))

	j.State = store.Merging
	if err := r.st.Update(j); err != nil {
		return err
	}
	// The next sweep picks the merge up; the state transition is durable
	// before any tooling runs, so a crash between the two resumes cleanly.
	return nil
}

// mergeStaged combines the verified batch into the one .m4b and hands it to
// tagging.
func (r *Runner) mergeStaged(ctx context.Context, j *store.Job) error {
	if r.merger == nil {
		return nil // not wired in this deployment shape; park
	}
	paths, err := r.stagedPaths(ctx, j)
	if err != nil {
		return err
	}

	res, err := r.merger.Merge(ctx, j.Title, paths)
	if err != nil {
		return r.fail(j, "merge failed: "+err.Error())
	}
	_ = r.st.AddEvent(j.ID, "merge", fmt.Sprintf(
		"%s (%d input files)", map[bool]string{true: "merged", false: "passed through"}[res.Merged], len(paths)))

	j.State = store.Tagging
	j.Phase = res.Path
	if err := r.st.Update(j); err != nil {
		return err
	}
	return nil
}

// tagStaged writes the library metadata contract onto the merged file.
func (r *Runner) tagStaged(ctx context.Context, j *store.Job) error {
	if r.tagger == nil {
		return nil // not wired in this deployment shape; park
	}
	if j.Phase == "" {
		return r.fail(j, "tagging: no merged file recorded")
	}

	err := r.tagger.Tag(ctx, j.Phase, tag.Metadata{
		Title:          j.Title,
		Author:         j.Author,
		Series:         j.Series,
		SeriesSequence: j.SeriesSequence,
		Narrator:       j.Narrator,
		Publisher:      j.Publisher,
		PublishedYear:  j.PublishedYear,
	})
	if err != nil {
		return r.fail(j, "tagging failed: "+err.Error())
	}
	_ = r.st.AddEvent(j.ID, "tag", "library metadata written")

	j.State = store.Shelving
	if err := r.st.Update(j); err != nil {
		return err
	}
	return nil
}

// shelveStaged copies the tagged file into the library and scans ABS.
func (r *Runner) shelveStaged(ctx context.Context, j *store.Job) error {
	if r.shelver == nil {
		return nil // not wired in this deployment shape; park
	}
	if j.Phase == "" {
		return r.fail(j, "shelving: no tagged file recorded")
	}

	dest, err := r.shelver.Shelve(ctx, j.Phase, shelve.Tagged{
		Title:  j.Title,
		Author: j.Author,
		Series: j.Series,
	})
	if err != nil {
		// The book may already be in the library (the scan error carries
		// that separately); a shelving failure is a job failure either way.
		return r.fail(j, "shelving failed: "+err.Error())
	}
	_ = r.st.AddEvent(j.ID, "shelve", "landed at "+dest)

	// The staging copy is scratch: the library file is the deliverable.
	// A failed removal never undoes the shelving — a leftover costs disk
	// space, not correctness.
	if err := os.Remove(j.Phase); err != nil && !os.IsNotExist(err) {
		log.Printf("job: %s: staging cleanup: %v", j.ID[:8], err)
	}

	j.State = store.Done
	j.Phase = dest
	if err := r.st.Update(j); err != nil {
		return err
	}
	_ = r.st.AddEvent(j.ID, "done", "in the library")
	r.notify(ctx, "Book shelved", j.Title+" is in the library")
	return nil
}

// stagedPaths locates the completed files through the transport's API — the
// same resolution the verify step uses, so merge and verify can never
// disagree about what was downloaded.
func (r *Runner) stagedPaths(ctx context.Context, j *store.Job) ([]string, error) {
	g, ok := r.grabbers[r.grabKindOf(j)]
	if !ok {
		return nil, fmt.Errorf("grabber %q vanished mid-job", j.Grabber)
	}
	paths, err := g.Located(ctx, grab.ID(j.GrabID))
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no staged files located for the merge")
	}
	return paths, nil
}

// notify pushes one message; failures are logged, never fatal.
func (r *Runner) notify(ctx context.Context, title, message string) {
	if r.notifier == nil {
		return
	}
	if err := r.notifier.Send(ctx, title, message); err != nil {
		log.Printf("job: notify: %v", err)
	}
}

// parkBackInConfirm reopens the confirm screen after a failed verification:
// the confirmed candidate is rejected with the verdict recorded, and the job
// returns to confirm where the remaining offers await the user.
func (r *Runner) parkBackInConfirm(j *store.Job, reason string) error {
	if j.ConfirmedCandidateID != "" {
		if err := r.st.RejectConfirmedCandidate(j.ID, j.ConfirmedCandidateID); err != nil {
			return err
		}
	}
	j.State = store.Confirm
	j.ConfirmedCandidateID = ""
	j.GrabID = ""
	j.Phase = ""
	j.Progress = 0
	j.Error = reason
	if err := r.st.Update(j); err != nil {
		return err
	}
	left, err := r.st.CandidateCount(j.ID)
	if err != nil {
		return err
	}
	if left == 0 {
		return r.fail(j, reason+"; no candidates remain")
	}
	return r.st.AddEvent(j.ID, "verify", fmt.Sprintf("candidate rejected; %d offers remain on the confirm screen", left))
}

// verifyReleaseOf maps the stored job onto the verify contract.
func verifyReleaseOf(j *store.Job) verify.Release {
	return verify.Release{
		Title:          j.Title,
		Author:         j.Author,
		Series:         j.Series,
		SeriesSequence: j.SeriesSequence,
		Narrator:       j.Narrator,
		Publisher:      j.Publisher,
		Unabridged:     true,
	}
}

// firstNegative returns the first hard-failure reason for the audit line.
func firstNegative(reasons []string) string {
	for _, r := range reasons {
		if strings.HasPrefix(r, "✗") {
			return strings.TrimPrefix(r, "✗ ")
		}
	}
	return "unknown mismatch"
}

// fail marks a job failed with a reason and an audit entry.
func (r *Runner) fail(j *store.Job, reason string) error {
	j.State = store.Failed
	j.Error = reason
	j.Attempts++
	if err := r.st.Update(j); err != nil {
		return err
	}
	_ = r.st.AddEvent(j.ID, "failed", reason)
	r.notify(context.Background(), "Request failed", j.Title+": "+reason)
	return nil
}

// releaseOf maps the stored job onto the Release a source searches for.
func releaseOf(j *store.Job) source.Release {
	return source.Release{
		Title:          j.Title,
		Author:         j.Author,
		Series:         j.Series,
		SeriesSequence: j.SeriesSequence,
		Narrator:       j.Narrator,
		Publisher:      j.Publisher,
		// Catalogues treat editions as unabridged unless they say otherwise;
		// an abridged listing contradicts that and scoring flags it.
		Unabridged: true,
		Identifier: sourceIdentifier(j),
		Query:      j.Query,
	}
}

// sourceIdentifier round-trips the stored identifier kind/value.
func sourceIdentifier(j *store.Job) metadata.Identifier {
	if j.IdentifierKind == "" || j.IdentifierVal == "" {
		return metadata.Identifier{}
	}
	return metadata.Identifier{
		Kind:  metadata.IdentifierKind(j.IdentifierKind),
		Value: j.IdentifierVal,
	}
}

// renderSize makes a byte count human for the audit log.
func renderSize(n int64) string {
	switch {
	case n > 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n > 1<<20:
		return fmt.Sprintf("%.0f MiB", float64(n)/(1<<20))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// grabKindOf recovers the transport from the grabber name.
//
// The job row stores the grabber's name, not its Kind, because the name is
// what the audit shows. The runner looks the Kind back up through its map —
// which is why a vanished grabber is reported rather than guessed.
func (r *Runner) grabKindOf(j *store.Job) source.Kind {
	for k, g := range r.grabbers {
		if g.Name() == j.Grabber {
			return k
		}
	}
	return ""
}

// candidateUser is the peer identity of a live candidate. Sources that do
// not name peers leave it empty; the store row keeps it for display.
func candidateUser(c source.Candidate) string {
	if ref, ok := c.Handle.Ref.(interface{ Peer() string }); ok {
		return ref.Peer()
	}
	return ""
}

// toSourceFiles converts stored files to the source package's shape.
func toSourceFiles(files []store.CandidateFile) []source.File {
	out := make([]source.File, 0, len(files))
	for _, f := range files {
		out = append(out, source.File{Name: f.Name, Size: f.Size})
	}
	return out
}

// filesOf extracts the offer's file list in its stored shape.
func filesOf(c source.Candidate) []store.CandidateFile {
	out := make([]store.CandidateFile, 0, len(c.Files))
	for _, f := range c.Files {
		out = append(out, store.CandidateFile{Name: f.Name, Size: f.Size})
	}
	return out
}

// rebuilder is implemented by sources that can reconstruct a Grab handle
// from a persisted candidate. The username is stored data; the handle shape
// is the source's private business, so the conversion lives behind the
// interface rather than in the runner.
type rebuilder interface {
	Rebuild(username string, files []store.CandidateFile) source.Handle
}

// toSourceCandidate rebuilds a source.Candidate from a persisted row so the
// origin source can enqueue the user's confirmed selection.
//
// Only the fields Grab needs are populated: the enqueue replays filenames and
// sizes; the scorer's derived summaries are not needed to move bytes.
func toSourceCandidate(c *store.Candidate, files []store.CandidateFile, origin source.Source) (source.Candidate, error) {
	rb, ok := origin.(rebuilder)
	if !ok {
		return source.Candidate{}, fmt.Errorf("source %q cannot rebuild persisted candidates", c.Source)
	}
	return source.Candidate{
		Source:    c.Source,
		Title:     c.Title,
		Format:    c.Format,
		Bitrate:   c.Bitrate,
		SizeBytes: c.SizeBytes,
		Files:     toSourceFiles(files),
		RemoteDir: c.RemoteDir,
		Handle:    rb.Rebuild(c.Username, files),
	}, nil
}
