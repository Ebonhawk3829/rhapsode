package job

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ebonhawk3829/rhapsode/internal/grab"
	"github.com/Ebonhawk3829/rhapsode/internal/merge"
	"github.com/Ebonhawk3829/rhapsode/internal/shelve"
	"github.com/Ebonhawk3829/rhapsode/internal/source"
	"github.com/Ebonhawk3829/rhapsode/internal/store"
	"github.com/Ebonhawk3829/rhapsode/internal/tag"
	"github.com/Ebonhawk3829/rhapsode/internal/verify"
)

// fakeSource is a scripted source.
type fakeSource struct {
	name    string
	cands   []source.Candidate
	err     error
	grabs   int
	lastRel source.Release
}

func (f *fakeSource) Name() string { return f.name }
func (f *fakeSource) Kind() source.Kind {
	return source.P2P
}
func (f *fakeSource) Search(_ context.Context, rel source.Release) ([]source.Candidate, error) {
	f.lastRel = rel
	if f.err != nil {
		return nil, f.err
	}
	return f.cands, nil
}
func (f *fakeSource) Grab(context.Context, source.Candidate, string) (source.Handle, error) {
	f.grabs++
	return source.Handle{Kind: source.P2P, Ref: p2pTestRef{}}, nil
}

// Rebuild satisfies the runner's rebuilder contract.
func (f *fakeSource) Rebuild(username string, _ []store.CandidateFile) source.Handle {
	return source.Handle{Kind: source.P2P, Ref: p2pTestRef{username}}
}

type p2pTestRef struct{ n string }

// Peer names the fake peer so distinct offers persist as distinct rows (the
// store collapses identical job/source/username/remote_dir offers).
func (r p2pTestRef) Peer() string { return r.n }

// fakeGrabber is a scripted downloader.
type fakeGrabber struct {
	name   string
	kinds  source.Kind
	states []grab.State
	adds   int
	fails  error
	// dir is where Located materialises its staged file (a real file, so
	// the merge pass-through copy works).
	dir string
}

func (f *fakeGrabber) Name() string      { return f.name }
func (f *fakeGrabber) Kind() source.Kind { return f.kinds }
func (f *fakeGrabber) Add(context.Context, source.Handle) (grab.ID, error) {
	f.adds++
	if f.fails != nil {
		return "", f.fails
	}
	return grab.ID("xfer-1"), nil
}
func (f *fakeGrabber) Status(context.Context, grab.ID) (grab.State, error) {
	if len(f.states) == 0 {
		return grab.State{Phase: grab.Unknown}, nil
	}
	st := f.states[0]
	if len(f.states) > 1 {
		f.states = f.states[1:]
	}
	return st, nil
}

// Located satisfies the grabber contract: it materialises a real file so
// the merge pass-through (which copies the located file) works in tests.
func (f *fakeGrabber) Located(_ context.Context, _ grab.ID) ([]string, error) {
	if f.dir == "" {
		f.dir = os.TempDir()
	}
	path := filepath.Join(f.dir, "the-black-prism.m4b")
	if err := os.WriteFile(path, []byte("staged-bytes"), 0o644); err != nil {
		return nil, err
	}
	return []string{path}, nil
}

func (f *fakeGrabber) Cancel(context.Context, grab.ID) error { return nil }

func newTestRunner(t *testing.T, srcs []source.Source, g grab.Grabber) (*Runner, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	r := New(st, srcs, map[source.Kind]grab.Grabber{source.P2P: g})
	// Tests script every tool: the production runners shell out to binaries
	// the test environment does not provide.
	r.SetVerifier(verify.New(func(string) (string, error) { return passingDump, nil }))
	r.SetMerger(scriptedMerger(t, st))
	r.SetTagger(scriptedTagger())
	r.SetShelver(scriptedShelver(t))
	return r, st
}

func indexOf(args []string, want string) int {
	for i, a := range args {
		if a == want {
			return i
		}
	}
	return -1
}

// scriptedMerger passes single files through and "merges" multi-file batches
// by concatenating the inputs into the output path the real tool would write.
func scriptedMerger(t *testing.T, st *store.Store) *merge.Merger {
	t.Helper()
	m := merge.New(t.TempDir())
	m.SetRunner(func(_ context.Context, args []string) error {
		out := args[indexOf(args, "--output-file")+1]
		return os.WriteFile(out, []byte("merged-bytes"), 0o644)
	})
	return m
}

// scriptedTagger accepts every tag run.
func scriptedTagger() *tag.Tagger {
	tg := tag.New()
	tg.SetRunner(func(context.Context, []string) error { return nil })
	return tg
}

// scriptedShelver copies into a temp library and accepts every ABS call.
func scriptedShelver(t *testing.T) *shelve.Shelver {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return shelve.New(t.TempDir(), srv.URL, "token", "lib-1")
}

// passingDump is a tone dump document the seeded test job passes against
// ("The Black Prism", no author/narrator on the job row — only the title and
// unabridged checks apply).
const passingDump = `{"audio": {"duration": 1000}, "meta": {"title": "The Black Prism", "chapters": []}}`

func seedJob(t *testing.T, st *store.Store) *store.Job {
	t.Helper()
	j := &store.Job{
		Title:  "The Black Prism",
		Author: "Brent Weeks",
		Slug:   "the-black-prism-9781648794384",
	}
	if err := st.Create(j); err != nil {
		t.Fatalf("create: %v", err)
	}
	return j
}

// confirmAll drives a searched job through the user's confirmation. The
// confirm state is not a sweep target: reaching downloading requires the
// user's pick, which tests supply here to exercise the rest of the machine.
func confirmAll(t *testing.T, r *Runner, st *store.Store, j *store.Job) *store.Job {
	t.Helper()
	got, err := st.Get(j.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if got.State != store.Confirm {
		t.Fatalf("state = %v, want confirm before confirming", got.State)
	}
	cands, err := st.Candidates(j.ID, 0)
	if err != nil {
		t.Fatalf("candidates: %v", err)
	}
	if len(cands) == 0 {
		t.Fatal("no candidates to confirm")
	}
	out, err := r.Confirm(context.Background(), j.ID, cands[0].ID, nil)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	return out
}

func TestSearchGrabsBestCandidate(t *testing.T) {
	src := &fakeSource{
		name: "slskd",
		cands: []source.Candidate{
			{Source: "slskd", Title: "mp3 copy", Format: "mp3", SizeBytes: 300 << 20, Files: []source.File{{Name: "mp3 copy/The Black Prism.mp3", Size: 300 << 20}}, Handle: source.Handle{Kind: source.P2P, Ref: p2pTestRef{}}},
			{Source: "slskd", Title: "m4b copy", Format: "m4b", SizeBytes: 200 << 20, Files: []source.File{{Name: "m4b copy/The Black Prism.m4b", Size: 200 << 20}}, Handle: source.Handle{Kind: source.P2P, Ref: p2pTestRef{}}},
		},
	}
	g := &fakeGrabber{name: "slskd", kinds: source.P2P}
	r, st := newTestRunner(t, []source.Source{src}, g)
	j := seedJob(t, st)

	if err := r.advance(context.Background(), j); err != nil {
		t.Fatalf("advance: %v", err)
	}

	// Re-read the job: the state machine writes through the store.
	got, _ := st.Get(j.ID)
	if got.State != store.Confirm {
		t.Fatalf("state = %v, want confirm (search must not auto-grab)", got.State)
	}

	// Confirming the first candidate exercises the user's decision path; the
	// offer order itself is the scorer's concern, tested in its own package.
	got = confirmAll(t, r, st, j)
	if got.State != store.Downloading {
		t.Fatalf("state = %v, want downloading after confirm", got.State)
	}
	if got.Source != "slskd" {
		t.Errorf("source = %q", got.Source)
	}
	if got.GrabID != "xfer-1" {
		t.Errorf("grab id = %q", got.GrabID)
	}
	if got.ConfirmedCandidateID == "" {
		t.Error("confirmed candidate id not recorded on the job")
	}
	if g.adds != 1 {
		t.Errorf("Add called %d times, want 1", g.adds)
	}
}

func TestNoCandidatesFails(t *testing.T) {
	src := &fakeSource{name: "slskd"}
	g := &fakeGrabber{name: "slskd", kinds: source.P2P}
	r, st := newTestRunner(t, []source.Source{src}, g)
	j := seedJob(t, st)

	if err := r.advance(context.Background(), j); err != nil {
		t.Fatalf("advance: %v", err)
	}
	got, _ := st.Get(j.ID)
	if got.State != store.Failed {
		t.Fatalf("state = %v, want failed", got.State)
	}
	if got.Error == "" {
		t.Error("error is empty, want a reason")
	}
	_ = got
}

func TestDeadSourceDoesNotFailTheJob(t *testing.T) {
	dead := &fakeSource{name: "dead", err: fmt.Errorf("network down")}
	alive := &fakeSource{name: "alive", cands: []source.Candidate{
		{Source: "alive", Title: "copy", Format: "m4b", SizeBytes: 100 << 20, Files: []source.File{{Name: "copy/The Black Prism.m4b"}}, Handle: source.Handle{Kind: source.P2P, Ref: p2pTestRef{}}},
	}}
	g := &fakeGrabber{name: "slskd", kinds: source.P2P}
	r, st := newTestRunner(t, []source.Source{dead, alive}, g)
	j := seedJob(t, st)

	if err := r.advance(context.Background(), j); err != nil {
		t.Fatalf("advance: %v", err)
	}
	got, _ := st.Get(j.ID)
	if got.State != store.Confirm {
		t.Fatalf("state = %v, want confirm with offers from the surviving source", got.State)
	}
	cands, _ := st.Candidates(j.ID, 0)
	if len(cands) == 0 || cands[0].Source != "alive" {
		t.Fatalf("candidates = %d, want the surviving source's offer", len(cands))
	}
	got = confirmAll(t, r, st, j)
	if got.State != store.Downloading {
		t.Fatalf("state = %v, want downloading after confirm", got.State)
	}
}

func TestDownloadCompletes(t *testing.T) {
	src := &fakeSource{name: "slskd", cands: []source.Candidate{
		{Source: "slskd", Title: "copy", Format: "m4b", SizeBytes: 100 << 20, Files: []source.File{{Name: "copy/The Black Prism.m4b"}}, Handle: source.Handle{Kind: source.P2P, Ref: p2pTestRef{}}},
	}}
	g := &fakeGrabber{name: "slskd", kinds: source.P2P, states: []grab.State{
		{Phase: grab.Active, Progress: 0.5},
		{Phase: grab.Done, Progress: 1},
	}}
	r, st := newTestRunner(t, []source.Source{src}, g)
	j := seedJob(t, st)

	if err := r.advance(context.Background(), j); err != nil {
		t.Fatalf("advance 1: %v", err)
	}
	_ = confirmAll(t, r, st, j)
	j, _ = st.Get(j.ID)
	if err := r.advance(context.Background(), j); err != nil {
		t.Fatalf("advance 2: %v", err)
	}
	got, _ := st.Get(j.ID)
	if got.State != store.Downloading {
		t.Fatalf("state = %v after poll 1, want downloading", got.State)
	}
	if got.Phase != string(grab.Active) {
		t.Errorf("phase = %q, want active", got.Phase)
	}
	// The scripted pipeline runs through: a completed download is verified
	// and merged within successive sweeps, then parks in tag review for
	// the user's edit+confirm. Each advance is one lifecycle step, so
	// sweep until tag review, confirm the tags, then sweep to terminal.
	for {
		j, _ = st.Get(j.ID)
		if j.State.Terminal() || j.State == store.TagReview {
			break
		}
		if err := r.advance(context.Background(), j); err != nil {
			t.Fatalf("advance %s: %v", j.State, err)
		}
	}
	got, _ = st.Get(j.ID)
	if got.State != store.TagReview {
		t.Fatalf("state = %v, want tag-review (merge parks for tag confirm)", got.State)
	}
	if _, err := r.ConfirmTags(context.Background(), j.ID, tag.Metadata{
		Title: got.Title, Author: got.Author,
	}); err != nil {
		t.Fatalf("confirm tags: %v", err)
	}
	for {
		j, _ = st.Get(j.ID)
		if j.State.Terminal() {
			break
		}
		if err := r.advance(context.Background(), j); err != nil {
			t.Fatalf("advance %s: %v", j.State, err)
		}
	}
	got, _ = st.Get(j.ID)
	if got.State != store.Done {
		t.Errorf("state = %v, want done (scripted pipeline)", got.State)
	}
}

func TestDownloadFailureFailsJob(t *testing.T) {
	src := &fakeSource{name: "slskd", cands: []source.Candidate{
		{Source: "slskd", Title: "copy", Format: "m4b", Files: []source.File{{Name: "copy/The Black Prism.m4b"}}, Handle: source.Handle{Kind: source.P2P, Ref: p2pTestRef{}}},
	}}
	g := &fakeGrabber{name: "slskd", kinds: source.P2P, states: []grab.State{
		{Phase: grab.Failed, Message: "peer offline"},
	}}
	r, st := newTestRunner(t, []source.Source{src}, g)
	j := seedJob(t, st)

	if err := r.advance(context.Background(), j); err != nil {
		t.Fatalf("advance 1: %v", err)
	}
	_ = confirmAll(t, r, st, j)
	j, _ = st.Get(j.ID)
	if err := r.advance(context.Background(), j); err != nil {
		t.Fatalf("advance 2: %v", err)
	}
	got, _ := st.Get(j.ID)
	if got.State != store.Failed {
		t.Fatalf("state = %v, want failed", got.State)
	}
	if !strings.Contains(got.Error, "peer offline") {
		t.Errorf("error = %q, want the downloader's message", got.Error)
	}
}

func TestReleaseCarriesQuery(t *testing.T) {
	src := &fakeSource{name: "slskd"}
	g := &fakeGrabber{name: "slskd", kinds: source.P2P}
	r, st := newTestRunner(t, []source.Source{src}, g)
	j := seedJob(t, st)
	j.Query = "black prism dramatized"
	if err := st.Update(j); err != nil {
		t.Fatal(err)
	}

	_ = r.advance(context.Background(), j)
	rel := src.lastRel
	if rel.Query != "black prism dramatized" {
		t.Errorf("release query = %q, want the job's query", rel.Query)
	}
	if rel.Title != "The Black Prism" {
		t.Errorf("release title = %q", rel.Title)
	}
}

func TestUnknownKindFailsClearly(t *testing.T) {
	src := &fakeSource{
		name: "odd",
		cands: []source.Candidate{
			{Source: "odd", Title: "copy", Format: "m4b", Files: []source.File{{Name: "copy/x.m4b"}}, Handle: source.Handle{Kind: source.Direct}},
		},
	}
	g := &fakeGrabber{name: "slskd", kinds: source.P2P}
	r, st := newTestRunner(t, []source.Source{src}, g)
	j := seedJob(t, st)

	if err := r.advance(context.Background(), j); err != nil {
		t.Fatalf("advance: %v", err)
	}
	got, _ := st.Get(j.ID)
	if got.State != store.Confirm {
		t.Fatalf("state = %v, want confirm", got.State)
	}
	cands, _ := st.Candidates(j.ID, 0)
	if len(cands) == 0 {
		t.Fatal("no candidates stored")
	}
	if _, err := r.Confirm(context.Background(), j.ID, cands[0].ID, nil); err == nil {
		t.Fatal("confirm succeeded for an unsupported transport, want a clear error")
	} else if !strings.Contains(err.Error(), "direct") {
		t.Errorf("error = %q, want it to name the missing transport", err.Error())
	}
}

// downloadToVerifying drives a fresh job through search → confirm →
// download → done-poll, leaving it in verifying.
func downloadToVerifying(t *testing.T, r *Runner, st *store.Store, j *store.Job) *store.Job {
	t.Helper()
	if err := r.advance(context.Background(), j); err != nil {
		t.Fatalf("advance search: %v", err)
	}
	_ = confirmAll(t, r, st, j)
	j, _ = st.Get(j.ID)
	if err := r.advance(context.Background(), j); err != nil {
		t.Fatalf("advance poll: %v", err)
	}
	j, _ = st.Get(j.ID)
	if err := r.advance(context.Background(), j); err != nil {
		t.Fatalf("advance verify: %v", err)
	}
	got, _ := st.Get(j.ID)
	return got
}

func singleOfferRunner(t *testing.T, g grab.Grabber) (*Runner, *store.Store) {
	t.Helper()
	src := &fakeSource{name: "slskd", cands: []source.Candidate{
		{Source: "slskd", Title: "copy", Format: "m4b", SizeBytes: 100 << 20, Files: []source.File{{Name: "copy/The Black Prism.m4b"}}, Handle: source.Handle{Kind: source.P2P, Ref: p2pTestRef{}}},
	}}
	return newTestRunner(t, []source.Source{src}, g)
}

func TestVerifyPassAdvancesToMerging(t *testing.T) {
	g := &fakeGrabber{name: "slskd", kinds: source.P2P, states: []grab.State{
		{Phase: grab.Done, Progress: 1},
	}}
	r, st := singleOfferRunner(t, g)
	j := seedJob(t, st)

	got := downloadToVerifying(t, r, st, j)
	if got.State != store.Merging {
		t.Fatalf("state = %v, want merging (a verified batch advances)", got.State)
	}
	events, _ := st.Events(j.ID)
	found := false
	for _, e := range events {
		if e.Kind == "verify" && strings.Contains(e.Message, "verified") {
			found = true
		}
	}
	if !found {
		t.Errorf("no verify-pass event recorded: %+v", events)
	}
}

func TestVerifyFailureParksBackInConfirm(t *testing.T) {
	g := &fakeGrabber{name: "slskd", kinds: source.P2P, states: []grab.State{
		{Phase: grab.Done, Progress: 1},
	}}
	// Two offers: the confirmed one fails verification, the other stays on
	// the reopened confirm screen. (A single offer that fails verify fails
	// the job — nothing remains to choose — covered by the next test.)
	src := &fakeSource{name: "slskd", cands: []source.Candidate{
		{Source: "slskd", Title: "copy", Format: "m4b", SizeBytes: 100 << 20, Files: []source.File{{Name: "copy/The Black Prism.m4b"}}, Handle: source.Handle{Kind: source.P2P, Ref: p2pTestRef{"peer-a"}}},
		{Source: "slskd", Title: "second", Format: "mp3", SizeBytes: 80 << 20, Files: []source.File{{Name: "second/The Black Prism.mp3"}}, Handle: source.Handle{Kind: source.P2P, Ref: p2pTestRef{"peer-b"}}},
	}}
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	r := New(st, []source.Source{src}, map[source.Kind]grab.Grabber{source.P2P: g})
	r.SetVerifier(verify.New(func(string) (string, error) { return passingDump, nil }))
	// The staged tags contradict the release: the wrong edition arrived.
	r.SetVerifier(verify.New(func(string) (string, error) {
		return `{"audio": {"duration": 1000}, "meta": {"title": "Wrong Book", "chapters": []}}`, nil
	}))
	j := seedJob(t, st)

	got := downloadToVerifying(t, r, st, j)
	if got.State != store.Confirm {
		t.Fatalf("state = %v, want confirm (failed verify reopens the decision)", got.State)
	}
	// The stale banner is cleared: the verdict lives on the rejected row
	// and in the audit trail, not repeated above the live offers.
	if got.Error != "" {
		t.Errorf("error = %q, want empty (verdict is on the rejected row)", got.Error)
	}
	// The confirmed candidate carries the verify verdict: rejected_at is
	// stamped, while the confirmation stamp stays as the historical record
	// of what was tried and failed (the Candidates query keeps confirmed
	// rows visible so the job page still shows what went wrong). The
	// surviving offer remains selectable.
	cands, _ := st.Candidates(j.ID, 0)
	if len(cands) != 2 {
		t.Fatalf("candidates = %d, want the failed offer and the survivor", len(cands))
	}
	var rejected, selectable int
	for _, c := range cands {
		if !c.RejectedAt.IsZero() {
			rejected++
			if c.ConfirmedAt.IsZero() {
				t.Error("the offending candidate lost its confirmation record")
			}
		} else {
			selectable++
		}
	}
	if rejected != 1 || selectable != 1 {
		t.Errorf("rejected = %d, selectable = %d, want 1 and 1", rejected, selectable)
	}
	// Only the survivor is an available offer.
	left, _ := st.CandidateCount(j.ID)
	if left != 1 {
		t.Errorf("candidate count = %d, want 1 (the failed offer is off the table)", left)
	}
}

func TestVerifyFailureWithNoOffersFailsJob(t *testing.T) {
	g := &fakeGrabber{name: "slskd", kinds: source.P2P, states: []grab.State{
		{Phase: grab.Done, Progress: 1},
	}}
	r, st := singleOfferRunner(t, g)
	r.SetVerifier(verify.New(func(string) (string, error) {
		return `{"audio": {"duration": 1000}, "meta": {"title": "Wrong Book", "chapters": []}}`, nil
	}))
	j := seedJob(t, st)

	got := downloadToVerifying(t, r, st, j)
	if got.State != store.Failed {
		t.Fatalf("state = %v, want failed (the last offer failed verification)", got.State)
	}
	if !strings.Contains(got.Error, "no candidates remain") {
		t.Errorf("error = %q, want the exhausted-offers reason", got.Error)
	}
}

// fakeOwnedChecker scripts the duplicate guard.
type fakeOwnedChecker struct {
	owned bool
	err   error
}

func (f *fakeOwnedChecker) AlreadyOwned(context.Context, string) (bool, error) {
	return f.owned, f.err
}

func TestOwnedTitleFailsFast(t *testing.T) {
	g := &fakeGrabber{name: "slskd", kinds: source.P2P}
	r, st := singleOfferRunner(t, g)
	r.SetOwnedChecker(&fakeOwnedChecker{owned: true})
	j := seedJob(t, st)

	if err := r.advance(context.Background(), j); err != nil {
		t.Fatalf("advance: %v", err)
	}
	got, _ := st.Get(j.ID)
	if got.State != store.Failed {
		t.Fatalf("state = %v, want failed (the library already carries it)", got.State)
	}
	if !strings.Contains(got.Error, "already carries") {
		t.Errorf("error = %q, want the duplicate reason", got.Error)
	}
}

func TestUnreachableLibraryDoesNotBlockSearch(t *testing.T) {
	g := &fakeGrabber{name: "slskd", kinds: source.P2P}
	r, st := singleOfferRunner(t, g)
	r.SetOwnedChecker(&fakeOwnedChecker{err: fmt.Errorf("abs down")})
	j := seedJob(t, st)

	if err := r.advance(context.Background(), j); err != nil {
		t.Fatalf("advance: %v", err)
	}
	got, _ := st.Get(j.ID)
	if got.State != store.Confirm {
		t.Fatalf("state = %v, want confirm (an unreachable ABS must not block)", got.State)
	}
}

func TestMergeParksInTagReview(t *testing.T) {
	g := &fakeGrabber{name: "slskd", kinds: source.P2P, states: []grab.State{
		{Phase: grab.Done, Progress: 1},
	}}
	r, st := singleOfferRunner(t, g)
	j := seedJob(t, st)

	got := downloadToVerifying(t, r, st, j)
	if got.State != store.Merging {
		t.Fatalf("state = %v, want merging", got.State)
	}
	// One more advance runs the merge; the job parks in tag review with
	// the merged path recorded, awaiting the user's edit+confirm.
	if err := r.advance(context.Background(), got); err != nil {
		t.Fatalf("advance merge: %v", err)
	}
	got, _ = st.Get(j.ID)
	if got.State != store.TagReview {
		t.Fatalf("state = %v, want tag-review", got.State)
	}
	if got.Phase == "" {
		t.Error("phase empty, want the merged file path")
	}
	// Wrong-state confirm is refused.
	if _, err := r.ConfirmTags(context.Background(), "nope", tag.Metadata{}); err == nil {
		t.Error("ConfirmTags on missing job succeeded, want error")
	}
	// Edited values persist and advance to shelving; the next sweep
	// shelves through the scripted shelver.
	confirmed, err := r.ConfirmTags(context.Background(), j.ID, tag.Metadata{
		Title: "The Black Prism", Author: "Brent Weeks",
		Series: "Lightbringer", SeriesSequence: "1",
	})
	if err != nil {
		t.Fatalf("confirm tags: %v", err)
	}
	if confirmed.State != store.Shelving {
		t.Errorf("state = %v, want shelving after tag confirm", confirmed.State)
	}
	if confirmed.Series != "Lightbringer" || confirmed.SeriesSequence != "1" {
		t.Errorf("reviewed values not persisted: %+v", confirmed)
	}
	if err := r.advance(context.Background(), confirmed); err != nil {
		t.Fatalf("advance shelve: %v", err)
	}
	got, _ = st.Get(j.ID)
	if got.State != store.Done {
		t.Errorf("state = %v, want done", got.State)
	}
}

func TestRetryVerifyUsesLocalFiles(t *testing.T) {
	dir := t.TempDir()
	g := &fakeGrabber{name: "slskd", kinds: source.P2P}
	r, st := singleOfferRunner(t, g)
	r.SetDownloads(dir)
	j := seedJob(t, st)
	// Seed the search so a candidate with a real filename exists.
	if err := r.advance(context.Background(), j); err != nil {
		t.Fatalf("advance search: %v", err)
	}
	got0, _ := st.Get(j.ID)
	if got0.State != store.Confirm {
		t.Fatalf("state = %v, want confirm", got0.State)
	}
	cands, _ := st.Candidates(j.ID, 0)
	c := cands[0]
	audio := filepath.Join(dir, "audiobooks", j.Slug, filepath.Base(c.Files[0].Name))
	if err := os.MkdirAll(filepath.Dir(audio), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(audio, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := st.ConfirmCandidate(j.ID, c.ID, c.Files); err != nil {
		t.Fatal(err)
	}
	if err := st.RejectConfirmedCandidate(j.ID, c.ID); err != nil {
		t.Fatal(err)
	}
	got, err := r.RetryVerify(context.Background(), j.ID, c.ID)
	if err != nil {
		t.Fatalf("retry verify: %v", err)
	}
	if got.State != store.Verifying {
		t.Errorf("state = %v, want verifying (no re-download)", got.State)
	}
	// Missing files error instead of downloading silently.
	if _, err := r.RetryVerify(context.Background(), j.ID, "missing"); err == nil {
		t.Error("retry verify on missing candidate succeeded, want error")
	}
}
