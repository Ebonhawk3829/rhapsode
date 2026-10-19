package slskd

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Ebonhawk3829/rhapsode/internal/grab"
	"github.com/Ebonhawk3829/rhapsode/internal/source"
)

// TestBatchEnqueueCarriesDestination is the contract test for the property
// that has never been exercised against a live instance: the batch endpoint
// must receive the destination a job stages under. The per-peer endpoint
// silently ignores Options.Destination, so a test that only asserts "the
// request succeeded" would pass against either endpoint while only one of
// them lands the files in the right directory.
func TestBatchEnqueueCarriesDestination(t *testing.T) {
	var (
		gotDestination string
		gotUsername    string
		gotFiles       int
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v0/transfers/downloads/batches" {
			t.Errorf("path = %q, want the batch endpoint", r.URL.Path)
		}
		var req batchRequest
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		gotDestination = req.Options.Destination
		gotUsername = req.Username
		gotFiles = len(req.Files)

		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"batch":{"id":"batch-1"},"failures":[]}`))
	}))
	defer srv.Close()

	s := NewSource(New(srv.URL, "test-api-key"))

	cand := source.Candidate{
		Source: "slskd",
		Title:  "The Black Prism",
		Handle: source.Handle{
			Kind: source.P2P,
			Ref: p2pRef{
				Username: "somepeer",
				Files: []file{
					{Filename: "The Black Prism - Part 1.m4b", Size: 100},
					{Filename: "The Black Prism - Part 2.m4b", Size: 200},
				},
			},
		},
	}

	h, err := s.Grab(context.Background(), cand, "the-black-prism-72fc7a3d")
	if err != nil {
		t.Fatalf("Grab: %v", err)
	}
	if gotDestination != "audiobooks/the-black-prism-72fc7a3d" {
		t.Errorf("destination = %q, want audiobooks/the-black-prism-72fc7a3d", gotDestination)
	}
	if gotUsername != "somepeer" {
		t.Errorf("username = %q, want somepeer", gotUsername)
	}
	if gotFiles != 2 {
		t.Errorf("files = %d, want 2", gotFiles)
	}

	ref, ok := h.Ref.(grabRef)
	if !ok {
		t.Fatalf("handle ref is %T, want grabRef", h.Ref)
	}
	if ref.BatchID != "batch-1" {
		t.Errorf("BatchID = %q, want batch-1", ref.BatchID)
	}
}

// TestGrabRejectsForeignHandle covers the grabber-routing contract: a handle
// produced by a different source must be rejected, not dereferenced.
func TestGrabRejectsForeignHandle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the API should not be called")
	}))
	defer srv.Close()

	s := NewSource(New(srv.URL, "k"))
	_, err := s.Grab(context.Background(), source.Candidate{
		Handle: source.Handle{Kind: source.Torrent, Ref: "magnet:?xt=urn:btih:abc"},
	}, "slug-1")
	if err == nil {
		t.Fatal("Grab accepted a torrent handle, want an error")
	}
}

// TestAddAcceptsItsOwnHandle covers the grabber side: a handle Source.Grab
// produced must be accepted by Grabber.Add, and the id it returns is the
// batch ID — the unit one job enqueued — which Status and Located are later
// called with.
func TestAddAcceptsItsOwnHandle(t *testing.T) {
	g := NewGrabber(New("http://unused", "k"), "/downloads/slskd-complete", "/downloads/slskd-complete")

	h := source.Handle{
		Kind: source.P2P,
		Ref:  grabRef{Username: "somepeer", BatchID: "batch-1"},
	}
	id, err := g.Add(context.Background(), h)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if id != grab.ID("batch-1") {
		t.Errorf("id = %q, want batch-1", id)
	}

	_, err = g.Add(context.Background(), source.Handle{Kind: source.Torrent, Ref: "magnet"})
	if err == nil {
		t.Error("Add accepted a torrent handle, want an error")
	}

	// A handle without a batch has nothing to track: the peer alone would
	// couple this job's state to every other job sharing that peer.
	_, err = g.Add(context.Background(), source.Handle{Kind: source.P2P, Ref: grabRef{Username: "somepeer"}})
	if err == nil {
		t.Error("Add accepted a handle without a batch id, want an error")
	}
}

// TestSearchQueryFallsBackToRawQuery covers the free-text path: a release
// that matched no catalogue has no author or title, and the search must still
// happen rather than erroring out.
func TestSearchQueryFallsBackToRawQuery(t *testing.T) {
	rel := source.Release{Query: "black prism graphic audio"}
	if got := searchQuery(rel); got != "black prism graphic audio" {
		t.Errorf("searchQuery(free text) = %q", got)
	}

	rel = source.Release{Author: "Brent Weeks", Title: "The Black Prism"}
	if got := searchQuery(rel); got != "Brent Weeks The Black Prism" {
		t.Errorf("searchQuery(catalogued) = %q", got)
	}
}

func TestDestinationFor(t *testing.T) {
	if got := destinationFor("the-black-prism-72fc7a3d"); got != "audiobooks/the-black-prism-72fc7a3d" {
		t.Errorf("destinationFor = %q", got)
	}

	// A job with nothing to name goes to a fixed bucket rather than
	// producing an empty path component.
	if got := destinationFor(""); got != "audiobooks/unknown" {
		t.Errorf("destinationFor(empty) = %q, want audiobooks/unknown", got)
	}
}

func TestSlugify(t *testing.T) {
	cases := []struct{ in, want string }{
		{"The Black Prism", "the-black-prism"},
		{"Book: A Subtitle", "book-a-subtitle"},
		{"  Spaces  ", "spaces"},
		{"Apostrophe's", "apostrophe-s"},
		{"", ""},
	}
	for _, c := range cases {
		if got := slugify(c.in); got != c.want {
			t.Errorf("slugify(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		in   string
		want grab.Phase
	}{
		{"Completed, Succeeded", grab.Done},
		{"InProgress", grab.Active},
		{"Queued, Remotely", grab.Queued},
		{"Completed, Failed", grab.Failed},
		{"Errored", grab.Failed},
		{"Cancelled", grab.Failed},
		{"", grab.Unknown},
	}
	for _, c := range cases {
		if got := classify(c.in); got != c.want {
			t.Errorf("classify(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestAggregateAllFilesDone covers the multi-file rule: a peer serving three
// files is done only when all three are done. A test asserting on one file
// would let a job report success with two parts missing.
func TestAggregateAllFilesDone(t *testing.T) {
	transfers := []transfer{
		{Filename: "a.m4b", Size: 100, BytesTransferred: 100, State: "Completed, Succeeded"},
		{Filename: "b.m4b", Size: 100, BytesTransferred: 100, State: "Completed, Succeeded"},
		{Filename: "c.m4b", Size: 100, BytesTransferred: 100, State: "Completed, Succeeded"},
	}
	st := aggregate(transfers)
	if st.Phase != grab.Done {
		t.Errorf("Phase = %v, want done", st.Phase)
	}
	if st.Progress != 1 {
		t.Errorf("Progress = %v, want 1", st.Progress)
	}
	if st.BytesDone != 300 || st.BytesTotal != 300 {
		t.Errorf("bytes = %d/%d, want 300/300", st.BytesDone, st.BytesTotal)
	}
}

func TestAggregateIncomplete(t *testing.T) {
	transfers := []transfer{
		{Filename: "a.m4b", Size: 100, BytesTransferred: 100, State: "Completed, Succeeded"},
		{Filename: "b.m4b", Size: 100, BytesTransferred: 50, State: "InProgress"},
		{Filename: "c.m4b", Size: 100, BytesTransferred: 0, State: "Queued, Remotely"},
	}
	st := aggregate(transfers)
	if st.Phase != grab.Active {
		t.Errorf("Phase = %v, want active", st.Phase)
	}
	if st.Progress <= 0 || st.Progress >= 1 {
		t.Errorf("Progress = %v, want between 0 and 1", st.Progress)
	}
}

func TestAggregateAnyFailure(t *testing.T) {
	transfers := []transfer{
		{Filename: "a.m4b", Size: 100, BytesTransferred: 100, State: "Completed, Succeeded"},
		{Filename: "b.m4b", Size: 100, BytesTransferred: 0, State: "Completed, Failed"},
	}
	st := aggregate(transfers)
	if st.Phase != grab.Failed {
		t.Errorf("Phase = %v, want failed", st.Phase)
	}
	if st.Message == "" {
		t.Error("Message = empty, want the failing state")
	}
}

func TestStatusUnknownBatch(t *testing.T) {
	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.URL.Path != "/api/v0/transfers/downloads/batches/gone" {
			t.Errorf("path = %q, want the batch endpoint", r.URL.Path)
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	g := NewGrabber(New(srv.URL, "k"), "/downloads/slskd-complete", "/downloads/slskd-complete")
	st, err := g.Status(context.Background(), grab.ID("gone"))
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !called {
		t.Fatal("the API was not called")
	}
	if st.Phase != grab.Unknown {
		t.Errorf("Phase = %v, want unknown", st.Phase)
	}
}

// TestStatusViaBatch pins the routing: Status reads the batch the job
// enqueued, not the peer's whole transfer list. A peer serving two jobs at
// once must not make one job's Done depend on the other's files.
func TestStatusViaBatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v0/transfers/downloads/batches/b-1" {
			t.Errorf("path = %q, want the batch endpoint", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
			"id": "b-1", "username": "peer",
			"transfers": [
				{"filename": "a.m4b", "size": 100, "bytesTransferred": 100, "state": "Completed, Succeeded"},
				{"filename": "b.m4b", "size": 100, "bytesTransferred": 100, "state": "Completed, Succeeded"}
			]
		}`))
	}))
	defer srv.Close()

	g := NewGrabber(New(srv.URL, "k"), "/downloads/slskd-complete", "/downloads/slskd-complete")
	st, err := g.Status(context.Background(), grab.ID("b-1"))
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Phase != grab.Done {
		t.Errorf("Phase = %v, want done", st.Phase)
	}
}

// TestLocated pins the file-location contract: the completed paths come
// from the batch's own destination record joined with the remote file's
// base name, translated into rhapsode's view of the download tree.
func TestLocated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"id": "b-1", "username": "peer",
			"options": {"destination": "audiobooks/the-black-prism-72fc7a3d"},
			"transfers": [
				{"filename": "@@peer\\Audiobooks\\The Black Prism\\The Black Prism.m4b", "size": 100, "bytesTransferred": 100, "state": "Completed, Succeeded"},
				{"filename": "@@peer\\Audiobooks\\The Black Prism\\cover.jpg", "size": 10, "bytesTransferred": 0, "state": "Completed, Failed"}
			]
		}`))
	}))
	defer srv.Close()

	g := NewGrabber(New(srv.URL, "k"), "/downloads/slskd-complete", "/downloads")
	paths, err := g.Located(context.Background(), grab.ID("b-1"))
	if err != nil {
		t.Fatalf("Located: %v", err)
	}
	want := "/downloads/audiobooks/the-black-prism-72fc7a3d/The Black Prism.m4b"
	if len(paths) != 1 || paths[0] != want {
		t.Errorf("paths = %v, want [%q]", paths, want)
	}
}

func TestLocatedUnknownBatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	g := NewGrabber(New(srv.URL, "k"), "/downloads/slskd-complete", "/downloads")
	if _, err := g.Located(context.Background(), grab.ID("gone")); err == nil {
		t.Fatal("Located succeeded for an unknown batch, want an error")
	}
}

// TestLocalPathTranslation pins the prefix swap: slskd's root is the prefix
// it writes destinations under, and rhapsode's root is what replaces it.
func TestLocalPathTranslation(t *testing.T) {
	g := NewGrabber(New("http://unused", "k"), "/downloads/slskd-complete", "/downloads")
	got := g.localPath("audiobooks/x/file.m4b")
	if got != "/downloads/audiobooks/x/file.m4b" {
		t.Errorf("localPath = %q, want /downloads/audiobooks/x/file.m4b", got)
	}
}

func TestDedupe(t *testing.T) {
	mk := func(user, dir string) source.Candidate {
		return source.Candidate{
			RemoteDir: dir,
			Handle:    source.Handle{Kind: source.P2P, Ref: p2pRef{Username: user}},
		}
	}
	got := dedupe(
		[]source.Candidate{mk("a", "d1")},
		[]source.Candidate{mk("a", "d1"), mk("b", "d2")},
	)
	if len(got) != 2 {
		t.Fatalf("dedupe = %d, want 2 (same peer+dir collapses)", len(got))
	}
}
