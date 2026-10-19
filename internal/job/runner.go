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
	"os/exec"
	"path"
	"path/filepath"
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
	// downloads is rhapsode's view of the shared downloads tree, and the
	// per-transport complete dirs under it. slskd lands batches in
	// slskd-complete/audiobooks/<slug>; Transmission lands torrents in
	// kt-complete/<name>. The cleaner, the rescue scan, and the local-file
	// checks all resolve through these roots — never a bare
	// <downloads>/audiobooks join, which matches neither transport.
	// Empty means cleanup disabled (tests).
	downloads string
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

// SetDownloads wires the orphan cleaner: audiobooks/<slug> dirs under this
// tree are removed when their job ends. Empty disables cleanup.
func (r *Runner) SetDownloads(dir string) {
	r.downloads = dir
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

// Run sweeps until the context ends. A startup sweep first removes
// download dirs with no live job behind them: a crash between enqueue and
// cleanup leaves bytes no job will ever claim.
func (r *Runner) Run(ctx context.Context) {
	r.sweepOrphans()

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

// slugDirs returns the slug directory for each transport root: slskd
// nests under audiobooks/, Transmission lands torrent dirs directly under
// kt-complete. Callers check membership, not construction.
func (r *Runner) slugDirs(slug string) []string {
	if r.downloads == "" || slug == "" {
		return nil
	}
	return []string{
		filepath.Join(r.downloads, "slskd-complete", "audiobooks", slug),
		filepath.Join(r.downloads, "kt-complete", slug),
	}
}

// completeRoots returns the transport complete dirs for the rescue scan.
// Incomplete dirs are excluded deliberately: partial bytes are not
// verifiable, and the downloaders move finished files to -complete alone.
func (r *Runner) completeRoots() []string {
	if r.downloads == "" {
		return nil
	}
	return []string{
		filepath.Join(r.downloads, "slskd-complete", "audiobooks"),
		filepath.Join(r.downloads, "kt-complete"),
	}
}

// sweepOrphans removes slug dirs whose slug no live job holds.
// Terminal jobs own nothing: their dirs were removed at the transition, so
// anything left is crash debris.
func (r *Runner) sweepOrphans() {
	if r.downloads == "" {
		return
	}
	jobs, err := r.st.List("")
	if err != nil {
		log.Printf("job: orphan sweep: %v", err)
		return
	}
	live := map[string]bool{}
	for _, j := range jobs {
		if !j.State.Terminal() {
			live[j.Slug] = true
		}
	}
	for _, root := range r.completeRoots() {
		entries, err := os.ReadDir(root)
		if err != nil {
			if !os.IsNotExist(err) {
				log.Printf("job: orphan sweep %s: %v", root, err)
			}
			continue
		}
		for _, e := range entries {
			if !e.IsDir() || live[e.Name()] {
				continue
			}
			if err := os.RemoveAll(filepath.Join(root, e.Name())); err != nil {
				log.Printf("job: orphan sweep %s: %v", e.Name(), err)
			} else {
				log.Printf("job: orphan sweep removed %s", e.Name())
			}
		}
	}
}

// Orphans scans the transport complete dirs for directories no live job
// holds. Crash debris and manually-copied files both surface here; the
// caller decides which are rescues and which are rubbish.
func (r *Runner) Orphans() ([]store.Orphan, error) {
	if r.downloads == "" {
		return nil, nil
	}
	jobs, err := r.st.List("")
	if err != nil {
		return nil, err
	}
	live := map[string]bool{}
	for _, j := range jobs {
		if !j.State.Terminal() {
			live[j.Slug] = true
		}
	}
	var out []store.Orphan
	for _, root := range r.completeRoots() {
		entries, err := os.ReadDir(root)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, e := range entries {
			if !e.IsDir() || live[e.Name()] {
				continue
			}
			dir := filepath.Join(root, e.Name())
			o := store.Orphan{Slug: e.Name()}
			for _, f := range listAudio(dir) {
				info, err := os.Stat(f)
				if err != nil {
					continue
				}
				o.Files = append(o.Files, filepath.Base(f))
				o.SizeBytes += info.Size()
			}
			if len(o.Files) > 0 {
				out = append(out, o)
			}
		}
	}
	return out, nil
}

// ClaimOrphan attaches an orphan directory's audio files to a job in
// confirm, jumping it straight to verifying. The orphan dir's files become
// the confirmed selection of a synthetic candidate owned by the rescue, so
// the audit trail shows where the bytes came from. The directory itself is
// left in place until the job's normal cleanup removes it.
func (r *Runner) ClaimOrphan(ctx context.Context, jobID, slug string) (*store.Job, error) {
	_ = ctx
	j, err := r.st.Get(jobID)
	if err != nil {
		return nil, err
	}
	if j.State != store.Confirm {
		return nil, fmt.Errorf("job %s is in state %q, not confirm", shortID(j.ID), j.State)
	}
	// Resolve the orphan dir across both transport roots: slskd slug dirs
	// live under slskd-complete/audiobooks, torrent dirs directly under
	// kt-complete. First hit wins.
	var dir string
	for _, root := range r.completeRoots() {
		cand := filepath.Join(root, slug)
		if st, err := os.Stat(cand); err == nil && st.IsDir() {
			dir = cand
			break
		}
	}
	if dir == "" {
		return nil, fmt.Errorf("no orphan dir %s under the complete roots", slug)
	}
	audio := listAudio(dir)
	if len(audio) == 0 {
		return nil, fmt.Errorf("no audio files in %s", slug)
	}
	files := make([]store.CandidateFile, 0, len(audio))
	for _, f := range audio {
		info, err := os.Stat(f)
		if err != nil {
			continue
		}
		files = append(files, store.CandidateFile{Name: filepath.Base(f), Size: info.Size()})
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no audio files in %s", slug)
	}
	c := &store.Candidate{
		JobID:  j.ID,
		Source: "rescue",
		Kind:   string(source.P2P),
		Title:  "rescued from " + slug,
		Files:  files,
		Score:  0,
		Band:   "review",
		Reasons: []string{
			"· rescued from orphan dir " + slug,
			"· verify judges the files, not the provenance",
		},
	}
	if err := r.st.SaveCandidate(c); err != nil {
		return nil, err
	}
	if err := r.st.ConfirmCandidate(j.ID, c.ID, files); err != nil {
		return nil, err
	}
	j.State = store.Verifying
	j.Source = "rescue"
	j.ConfirmedCandidateID = c.ID
	j.GrabID = ""
	j.Phase = ""
	j.Progress = 0
	j.Error = ""
	if err := r.st.Update(j); err != nil {
		return nil, err
	}
	_ = r.st.AddEvent(j.ID, "grab", fmt.Sprintf("claimed orphan %s (%d files); verifying", slug, len(files)))
	return r.st.Get(jobID)
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
			log.Printf("job: %s (%s): %v", shortID(j.ID), j.Title, err)
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
		_ = r.st.AddEvent(j.ID, "search", fmt.Sprintf("source %s returned %d responses", s.Name(), len(cands)))
		for _, c := range cands {
			res := score.Grade(rel, c)
			if posted := candidatePosted(c); posted != "" {
				res.Reasons = append(res.Reasons, "· posted "+posted)
			}
			sc := &store.Candidate{
				JobID:       j.ID,
				Source:      c.Source,
				Kind:        string(c.Handle.Kind),
				Username:    candidateUser(c),
				Title:       c.Title,
				RemoteDir:   c.RemoteDir,
				ExternalURL: c.ExternalURL,
				Files:       filesOf(c),
				SizeBytes:   c.SizeBytes,
				Format:      c.Format,
				Bitrate:     c.Bitrate,
				// A free upload slot dominates peer ranking: the slskd
				// source offsets slotted peers by 1000 (see
				// slskd.freeSlotBonus) and this flag records it.
				FreeSlot: c.Seeders >= 1000,
				Score:    res.Score,
				Band:     res.Band,
				Reasons:  res.Reasons,
			}
			if err := r.st.SaveCandidate(sc); err != nil {
				return err
			}
			nFound++
		}
	}
	// nFound counts rows persisted this pass, including re-found offers the
	// store collapsed as duplicates. The audit line reports it as found,
	// not as new — the distinction matters when a re-sweep restates the
	// same list.
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
	return r.st.AddEvent(j.ID, "search", fmt.Sprintf("%d offers found; awaiting file selection", nFound))
}

// Download hands the user's file selection to the download pipeline.
//
// It is the resume action for a job awaiting file selection: the chosen
// candidate is marked, the selection enqueue happens through the
// candidate's recorded files, and the job proceeds to downloading. An empty
// selection means "take everything the offer showed", which is the usual
// case.
//
// When the selected files are already on disk under the job's slug dir
// (a re-check after a failed verify, a restart that lost the transfer
// record), the transfer is skipped and the job jumps straight to verifying.
// The UI names this with the candidate's own wording — "Download" when
// bytes are missing, "Re-check files" when they are present — but it is one
// action: prefer what exists, fetch what does not.
func (r *Runner) Confirm(ctx context.Context, jobID, candidateID string, selected []store.CandidateFile) (*store.Job, error) {
	j, err := r.st.Get(jobID)
	if err != nil {
		return nil, err
	}
	if j.State != store.Confirm {
		return nil, fmt.Errorf("job %s is in state %q, not confirm", shortID(j.ID), j.State)
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

	// Prefer what exists: when every selected file is already on disk
	// under the slug dir, skip the transfer and go straight to verifying.
	// This is the rescue path — a verify verdict the user overrode, a
	// restart that lost the transfer record — and it must never silently
	// re-download bytes the user already has.
	if paths := r.localSelection(j, files); paths != nil {
		j.State = store.Verifying
		j.Source = c.Source
		j.ConfirmedCandidateID = candidateID
		j.GrabID = ""
		j.Phase = ""
		j.Progress = 0
		j.Error = ""
		if err := r.st.Update(j); err != nil {
			return nil, err
		}
		_ = r.st.AddEvent(j.ID, "grab", fmt.Sprintf(
			"%d files already on disk; verifying without re-download", len(paths)))
		return r.st.Get(jobID)
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

	grabbable, err := origin.Grab(ctx, sc, j.Slug)
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
		return nil, fmt.Errorf("job %s is in state %q, not confirm", shortID(j.ID), j.State)
	}
	if err := r.st.RejectCandidate(jobID, candidateID); err != nil {
		return nil, err
	}
	left, err := r.st.CandidateCount(jobID)
	if err != nil {
		return nil, err
	}
	if left == 0 {
		if err := r.fail(j, "all candidates rejected"); err != nil {
			return nil, err
		}
		return j, nil
	}
	_ = r.st.AddEvent(j.ID, "reject", fmt.Sprintf("candidate %s rejected; %d remain", shortID(candidateID), left))
	return j, nil
}

// Unreject clears a candidate's rejected stamp so it can be confirmed again.
//
// Confirm-screen rejections and verify verdicts both land as rejected rows;
// the override does not distinguish them. The job must be in confirm —
// overriding mid-download would fork the pipeline — and the audit records
// the override explicitly.
func (r *Runner) Unreject(jobID, candidateID string) (*store.Job, error) {
	j, err := r.st.Get(jobID)
	if err != nil {
		return nil, err
	}
	if j.State != store.Confirm {
		return nil, fmt.Errorf("job %s is in state %q, not confirm", shortID(j.ID), j.State)
	}
	if err := r.st.Unreject(jobID, candidateID); err != nil {
		return nil, err
	}
	_ = r.st.AddEvent(j.ID, "unreject", fmt.Sprintf("candidate %s restored by user override", shortID(candidateID)))
	return r.st.Get(jobID)
}

// RetryVerify re-runs verification against files already on disk, without
// re-downloading. The restored candidate's selected files resolve under the
// job's slug dir; when every one exists locally, the job jumps straight to
// verifying instead of enqueueing a fresh transfer. When any file is
// missing (cleaned up, moved, never landed), it errors naming the absence
// rather than downloading silently — the user then confirms normally.
func (r *Runner) RetryVerify(ctx context.Context, jobID, candidateID string) (*store.Job, error) {
	j, err := r.st.Get(jobID)
	if err != nil {
		return nil, err
	}
	if j.State != store.Confirm {
		return nil, fmt.Errorf("job %s is in state %q, not confirm", shortID(j.ID), j.State)
	}
	c, err := r.st.CandidateByID(jobID, candidateID)
	if err != nil {
		return nil, err
	}
	files := c.SelectedFiles
	if len(files) == 0 {
		files = c.Files
	}
	if len(files) == 0 {
		return nil, errors.New("candidate has no files to verify")
	}
	paths := r.localSelection(j, files)
	if paths == nil {
		return nil, fmt.Errorf("files for %s are not on disk; download the offer to fetch them", shortID(candidateID))
	}
	if err := r.st.Unreject(jobID, candidateID); err != nil {
		return nil, err
	}
	if err := r.st.ConfirmCandidate(jobID, candidateID, files); err != nil {
		return nil, err
	}
	j.State = store.Verifying
	j.ConfirmedCandidateID = candidateID
	j.Error = ""
	if err := r.st.Update(j); err != nil {
		return nil, err
	}
	_ = r.st.AddEvent(j.ID, "verify", fmt.Sprintf("re-verifying %d local files (no re-download)", len(paths)))
	return r.st.Get(jobID)
}

// Cancel stops a job at the user's word. It is a decision, not an error:
// the audit records it as such. An in-flight transfer is stopped and its
// partial files deleted; a cancel failure is logged, never fatal — the
// orphan sweep collects whatever the downloader kept.
func (r *Runner) Cancel(jobID string) (*store.Job, error) {
	j, err := r.st.Get(jobID)
	if err != nil {
		return nil, err
	}
	if j.State.Terminal() {
		return j, nil
	}
	if j.GrabID != "" {
		if g, ok := r.grabbers[r.grabKindOf(j)]; ok {
			if err := g.Cancel(context.Background(), grab.ID(j.GrabID)); err != nil {
				log.Printf("job: %s: cancel transfer: %v", shortID(j.ID), err)
			}
		}
	}
	j.State = store.Cancelled
	if err := r.st.Update(j); err != nil {
		return nil, err
	}
	_ = r.st.AddEvent(j.ID, "cancel", "cancelled by user")
	r.cleanup(j)
	return j, nil
}

// Clear deletes a terminal job from the queue. Live jobs are refused —
// cancel first, so downloads and decisions are never orphaned by a delete.
// The download directory goes with the row: clearing is the user's
// explicit "I'm done with this", and leftovers are not souvenirs.
func (r *Runner) Clear(jobID string) error {
	j, err := r.st.Get(jobID)
	if err != nil {
		return err
	}
	if !j.State.Terminal() {
		return fmt.Errorf("job %s is %q, not terminal; cancel it first", shortID(j.ID), j.State)
	}
	r.cleanup(j)
	return r.st.Delete(jobID)
}

// SourceNames lists the configured source names for status display.
func (r *Runner) SourceNames() []string {
	names := make([]string, 0, len(r.sources))
	for _, s := range r.sources {
		names = append(names, s.Name())
	}
	return names
}

// OnDisk reports whether every named file exists under the job's slug dir.
func (r *Runner) OnDisk(jobID string, files []store.CandidateFile) bool {
	j, err := r.st.Get(jobID)
	if err != nil {
		return false
	}
	return r.localSelection(j, files) != nil
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

	prevPhase := j.Phase
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
	case grab.Unknown, grab.Stalled:
		// The downloader lost the transfer or parked it. Stay in
		// downloading but leave a trail: previously these phases spun
		// silently with no event, no failure, no notify. One event per
		// transition, not one per sweep.
		if prevPhase != string(st.Phase) {
			_ = r.st.AddEvent(j.ID, "download", fmt.Sprintf("transfer %s (%s)", st.Phase, st.Message))
		}
		return nil
	}
	return nil
}

// verifyStaged inspects the staged files' tags against the release.
//
// Verify advises, it never gates: mismatches persist as warnings for the
// tag-review screen, and every inspected batch proceeds to merging.
// Structural failures (no files, unreadable dumps, empty archives) are the
// only exits that fail or park.
func (r *Runner) verifyStaged(ctx context.Context, j *store.Job) error {
	if r.verifier == nil {
		return nil
	}
	var paths []string
	if j.GrabID == "" {
		// No transfer record: the rescue path put the job here straight
		// from confirm, so resolve the confirmed selection locally.
		c, err := r.st.CandidateByID(j.ID, j.ConfirmedCandidateID)
		if err != nil {
			return fmt.Errorf("locate staged files: %w", err)
		}
		files := c.SelectedFiles
		if len(files) == 0 {
			files = c.Files
		}
		paths = r.localSelection(j, files)
		if paths == nil {
			return r.fail(j, "local files missing for re-verify")
		}
	} else {
		g, ok := r.grabbers[r.grabKindOf(j)]
		if !ok {
			return r.fail(j, fmt.Sprintf("grabber %q vanished mid-job", j.Grabber))
		}
		var err error
		paths, err = g.Located(ctx, grab.ID(j.GrabID))
		if err != nil {
			return fmt.Errorf("locate staged files: %w", err)
		}
	}

	// Archives resolve here: a .zip holding the m4b extracts into the
	// slug dir, and verify inspects the contents. Torrents commonly wrap
	// the audio this way; grading the archive pre-grab was impossible,
	// so the honest check happens on the extracted files.
	var err error
	paths, err = extractArchives(paths)
	if err != nil {
		return r.fail(j, "extract archives: "+err.Error())
	}

	report, err := r.verifier.Inspect(paths, verifyReleaseOf(j))
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}

	// Verify advises, it never gates. The warnings persist on the job row
	// for the tag-review screen, where the human reads them next to the
	// proposed tags — the user already picked this file twice, and
	// encoder tagging is not ground truth worth overruling them with.
	// Structural failures (no files, unreadable dumps, empty archives)
	// errored above; everything that inspects proceeds to merging.
	if err := r.st.SaveVerifyWarnings(j.ID, report.Reasons); err != nil {
		return err
	}
	_ = r.st.AddEvent(j.ID, "verify", fmt.Sprintf(
		"inspected %d files (%s audio, %d chapters)",
		len(paths), verify.DurationString(report.DurationMS), report.Chapters))

	j.State = store.Merging
	if err := r.st.Update(j); err != nil {
		return err
	}
	// The next sweep picks the merge up; the state transition is durable
	// before any tooling runs, so a crash between the two resumes cleanly.
	return nil
}

// mergeStaged combines the verified batch into the one .m4b and parks the
// job in tag review: the user edits the proposed tags and confirms before
// anything is written.
//
// Paths resolve locally first: rescue-path jobs carry no GrabID, and
// transfer records expire (slskd retention) while the bytes remain. The
// transport API is the fallback for live transfers, not the primary.
func (r *Runner) mergeStaged(ctx context.Context, j *store.Job) error {
	if r.merger == nil {
		return nil // not wired in this deployment shape; park
	}
	paths, err := r.mergePaths(ctx, j)
	if err != nil {
		return err
	}

	res, err := r.merger.Merge(ctx, j.Slug, j.Title, paths)
	if err != nil {
		return r.fail(j, "merge failed: "+err.Error())
	}
	_ = r.st.AddEvent(j.ID, "merge", fmt.Sprintf(
		"%s (%d input files)", map[bool]string{true: "merged", false: "passed through"}[res.Merged], len(paths)))

	j.State = store.TagReview
	j.Phase = res.Path
	if err := r.st.Update(j); err != nil {
		return err
	}
	return r.st.AddEvent(j.ID, "tag-review", "merged; tags await review")
}

// VerifyWarnings returns the latest verify inspection's reasons for the
// tag-review screen. The warnings advise the human next to the proposed
// tags; they gated nothing to get here.
func (r *Runner) VerifyWarnings(jobID string) []string {
	w, err := r.st.VerifyWarnings(jobID)
	if err != nil {
		return nil
	}
	return w
}

// ConfirmTags applies the user's reviewed tags and advances to shelving.
// An empty field keeps the job's stored value: the review form submits the
// whole proposal, and clearing a field is how optional metadata is dropped.
func (r *Runner) ConfirmTags(ctx context.Context, jobID string, m tag.Metadata) (*store.Job, error) {
	j, err := r.st.Get(jobID)
	if err != nil {
		return nil, err
	}
	if j.State != store.TagReview {
		return nil, fmt.Errorf("job %s is in state %q, not tag-review", shortID(j.ID), j.State)
	}
	if j.Phase == "" {
		return nil, fmt.Errorf("job %s has no merged file to tag", shortID(j.ID))
	}
	// Persist the reviewed values so the shelving path and the audit agree
	// with what was written.
	j.Title = strings.TrimSpace(m.Title)
	j.Author = strings.TrimSpace(m.Author)
	j.Series = strings.TrimSpace(m.Series)
	j.SeriesSequence = strings.TrimSpace(m.SeriesSequence)
	j.Narrator = strings.TrimSpace(m.Narrator)
	j.Publisher = strings.TrimSpace(m.Publisher)
	j.PublishedYear = strings.TrimSpace(m.PublishedYear)
	j.Description = strings.TrimSpace(m.Description)
	j.Genre = strings.TrimSpace(m.Genre)

	j.State = store.Tagging
	if err := r.st.UpdateTags(j); err != nil {
		return nil, err
	}
	_ = r.st.AddEvent(j.ID, "tag-review", "tags confirmed by user")
	if err := r.tagStaged(ctx, j); err != nil {
		return nil, err
	}
	return r.st.Get(jobID)
}

// tagStaged writes the library metadata contract onto the merged file.
//
// One retry on tool failure: tone occasionally crashes transiently (a null
// reference on first open that passes on retry with identical input), and
// failing the job — with a push alert — on a flake wastes a verified,
// merged download. A second consecutive failure is real and fails the job.
func (r *Runner) tagStaged(ctx context.Context, j *store.Job) error {
	if r.tagger == nil {
		return nil // not wired in this deployment shape; park
	}
	if j.Phase == "" {
		return r.fail(j, "tagging: no merged file recorded")
	}

	meta := tag.Metadata{
		Title:          j.Title,
		Author:         j.Author,
		Series:         j.Series,
		SeriesSequence: j.SeriesSequence,
		Narrator:       j.Narrator,
		Publisher:      j.Publisher,
		PublishedYear:  j.PublishedYear,
		Description:    j.Description,
		Genre:          j.Genre,
	}
	if err := r.tagger.Tag(ctx, j.Phase, meta); err != nil {
		_ = r.st.AddEvent(j.ID, "tag", "first tag attempt failed, retrying: "+err.Error())
		if err2 := r.tagger.Tag(ctx, j.Phase, meta); err2 != nil {
			return r.fail(j, "tagging failed twice: "+err2.Error())
		}
		_ = r.st.AddEvent(j.ID, "tag", "retry succeeded")
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
		log.Printf("job: %s: staging cleanup: %v", shortID(j.ID), err)
	}
	// The download directory served its purpose: the bytes are in the
	// library now, and the dir goes back to empty scratch.
	r.cleanup(j)

	j.State = store.Done
	j.Phase = dest
	if err := r.st.Update(j); err != nil {
		return err
	}
	_ = r.st.AddEvent(j.ID, "done", "in the library")
	r.notify(ctx, "Book shelved", j.Title+" is in the library")
	return nil
}

// localSelection resolves the selected files against the slug dir,
// returning their paths when every one exists locally, nil otherwise.
// Basename matching is deliberate: the candidate records source-side paths
// (peer folders, torrent members) while the slug dir holds what landed.
// The basename itself is separator-normalised first: Windows peers report
// backslash paths, and path.Base alone would keep the whole string as the
// "name", never matching the landed file.
func (r *Runner) localSelection(j *store.Job, files []store.CandidateFile) []string {
	dirs := r.slugDirs(j.Slug)
	if len(dirs) == 0 {
		return nil
	}
	paths := make([]string, 0, len(files))
	for _, f := range files {
		base := path.Base(strings.ReplaceAll(f.Name, "\\", "/"))
		var hit string
		for _, dir := range dirs {
			p := filepath.Join(dir, base)
			if _, err := os.Stat(p); err == nil {
				hit = p
				break
			}
		}
		if hit == "" {
			return nil
		}
		paths = append(paths, hit)
	}
	return paths
}

// mergePaths resolves the files to merge: the confirmed selection against
// the slug dir first (bytes on disk are ground truth), the transport API
// second (live transfers whose batch record still exists). Verify used the
// same files to inspect, so merge and verify can never disagree.
func (r *Runner) mergePaths(ctx context.Context, j *store.Job) ([]string, error) {
	c, err := r.st.CandidateByID(j.ID, j.ConfirmedCandidateID)
	if err == nil {
		files := c.SelectedFiles
		if len(files) == 0 {
			files = c.Files
		}
		if paths := r.localSelection(j, files); paths != nil {
			return paths, nil
		}
	}
	return r.stagedPaths(ctx, j)
}

// stagedPaths locates the completed files through the transport's API — the
// fallback when the confirmed selection is not on disk (live transfer, or
// a rescue that moved bytes after confirming).
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

// extractArchives unpacks archive members found among the staged paths into
// their containing directory, returning the audio files within. Non-archive
// paths pass through untouched. Extraction shells to 7zz (in the image via
// the 7zip package): it handles zip, rar, and 7z uniformly, which the stdlib
// cannot. 7zz extracts flat into the slug dir, so traversal entries cannot
// escape it. A member list with no audio inside is an error — verify cannot
// judge an empty set, and failing here names the cause instead of parking
// on it.
func extractArchives(paths []string) ([]string, error) {
	var out []string
	for _, p := range paths {
		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(p), "."))
		if !archiveExts[ext] {
			out = append(out, p)
			continue
		}
		dir := filepath.Dir(p)
		before := listAudio(dir)
		cmd := exec.CommandContext(context.Background(), "7zz", "x", "-y", "-o"+dir, p)
		if raw, err := cmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("extract %s: %v: %.300s", filepath.Base(p), err, raw)
		}
		seen := map[string]bool{}
		for _, f := range before {
			seen[f] = true
		}
		var found []string
		for _, f := range listAudio(dir) {
			if !seen[f] {
				found = append(found, f)
			}
		}
		if len(found) == 0 {
			return nil, fmt.Errorf("%s holds no audio", filepath.Base(p))
		}
		out = append(out, found...)
	}
	return out, nil
}

// listAudio returns the audio files directly inside dir (no recursion: the
// slug dir is flat by construction, and archives extract into it).
func listAudio(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(e.Name()), "."))
		if audioExts[ext] {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}

// audioExts mirrors the scorer's listenable set: extraction keeps what
// verify can judge.
var audioExts = map[string]bool{
	"m4b": true, "m4a": true, "mp3": true, "flac": true,
	"m4p": true, "ogg": true, "oga": true, "opus": true,
	"wma": true, "wav": true, "aiff": true, "aif": true,
}

var archiveExts = map[string]bool{
	"zip": true, "rar": true, "7z": true, "tar": true, "gz": true,
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

// fail marks a job failed with a reason and an audit entry. The job's
// download directory is removed: a failed acquisition leaves no bytes
// behind for the next attempt to trip over.
func (r *Runner) fail(j *store.Job, reason string) error {
	j.State = store.Failed
	j.Error = reason
	j.Attempts++
	if err := r.st.Update(j); err != nil {
		return err
	}
	_ = r.st.AddEvent(j.ID, "failed", reason)
	r.cleanup(j)
	r.notify(context.Background(), "Request failed", j.Title+": "+reason)
	return nil
}

// cleanup removes the job's download directories across both transport
// roots. A removal failure is logged, never fatal: leftovers cost disk,
// not correctness, and the startup sweep retries them.
func (r *Runner) cleanup(j *store.Job) {
	for _, dir := range r.slugDirs(j.Slug) {
		if err := os.RemoveAll(dir); err != nil {
			log.Printf("job: %s: cleanup %s: %v", shortID(j.ID), dir, err)
		}
	}
}

// releaseOf maps the stored job onto the Release a source searches for.
//
// Unabridged is always true: neither catalogue surfaces an abridgement flag
// on its records, and every edition rhapsode acquires is produced and sold
// as unabridged. If a catalogue ever reports otherwise, this becomes a
// stored field. Until then the assumption lives here, once, rather than
// scattered across call sites.
func releaseOf(j *store.Job) source.Release {
	return source.Release{
		Title:          j.Title,
		Author:         j.Author,
		Series:         j.Series,
		SeriesSequence: j.SeriesSequence,
		Narrator:       j.Narrator,
		Publisher:      j.Publisher,
		Unabridged:     true,
		Identifier:     sourceIdentifier(j),
		Query:          j.Query,
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

// shortID renders an ID prefix for logs and audit lines. IDs arriving over
// the API are user input and may be short or empty; slicing without a guard
// panics the process.
func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
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

// candidatePosted extracts a listing's posted date for display. ABB
// listings carry it; slskd shares have no equivalent. The date rides a
// reason line, never the score — recency is a hint about swarm liveness,
// not evidence of edition match.
func candidatePosted(c source.Candidate) string {
	if p, ok := c.Handle.Ref.(interface{ PostedDateOf() string }); ok {
		return p.PostedDateOf()
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
