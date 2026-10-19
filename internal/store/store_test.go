package store

import (
	"testing"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	st, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func mkJob(slug string) *Job {
	return &Job{Title: "The Black Prism", Author: "Brent Weeks", Slug: slug}
}

func TestCreateGetListUpdate(t *testing.T) {
	st := openTest(t)
	j := mkJob("the-black-prism-aaa11111")
	if err := st.Create(j); err != nil {
		t.Fatalf("create: %v", err)
	}
	if j.ID == "" {
		t.Fatal("create did not assign an ID")
	}
	got, err := st.Get(j.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != "The Black Prism" || got.State != Pending {
		t.Errorf("got %+v, want title + pending", got)
	}
	got.State = Searching
	if err := st.Update(got); err != nil {
		t.Fatalf("update: %v", err)
	}
	got2, _ := st.Get(j.ID)
	if got2.State != Searching {
		t.Errorf("state = %v, want searching", got2.State)
	}
	all, err := st.List("")
	if err != nil || len(all) != 1 {
		t.Fatalf("list = %d, %v; want 1", len(all), err)
	}
	filtered, _ := st.List(Searching)
	if len(filtered) != 1 {
		t.Errorf("filtered list = %d, want 1", len(filtered))
	}
	none, _ := st.List(Done)
	if len(none) != 0 {
		t.Errorf("done list = %d, want 0", len(none))
	}
	if _, err := st.Get("does-not-exist"); err != ErrNotFound {
		t.Errorf("get missing = %v, want ErrNotFound", err)
	}
}

func TestCreateRequiresSlug(t *testing.T) {
	st := openTest(t)
	if err := st.Create(&Job{Title: "x"}); err == nil {
		t.Error("create without slug succeeded, want error")
	}
}

func TestCandidatesIsolatedPerJob(t *testing.T) {
	st := openTest(t)
	a := mkJob("job-a-11111111")
	b := mkJob("job-b-22222222")
	if err := st.Create(a); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(b); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveCandidate(&Candidate{
		JobID: a.ID, Source: "slskd", Kind: "p2p", Username: "peer-a",
		Title: "offer a", RemoteDir: "dir-a",
		Files: []CandidateFile{{Name: "dir-a/file.m4b", Size: 10}},
		Score: 0.9, Band: "accept",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveCandidate(&Candidate{
		JobID: b.ID, Source: "slskd", Kind: "p2p", Username: "peer-b",
		Title: "offer b", RemoteDir: "dir-b",
		Files: []CandidateFile{{Name: "dir-b/file.m4b", Size: 10}},
		Score: 0.5, Band: "review",
	}); err != nil {
		t.Fatal(err)
	}

	candsA, err := st.Candidates(a.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(candsA) != 1 || candsA[0].Title != "offer a" {
		t.Fatalf("candidates(a) = %v, want only offer a", titles(candsA))
	}
	candsB, err := st.Candidates(b.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(candsB) != 1 || candsB[0].Title != "offer b" {
		t.Fatalf("candidates(b) = %v, want only offer b", titles(candsB))
	}
	if n, _ := st.CandidateCount(a.ID); n != 1 {
		t.Errorf("count(a) = %d, want 1", n)
	}
}

func titles(cs []*Candidate) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Title)
	}
	return out
}

func TestSaveCandidateIdempotent(t *testing.T) {
	st := openTest(t)
	j := mkJob("job-dedupe-33333333")
	if err := st.Create(j); err != nil {
		t.Fatal(err)
	}
	mk := func() *Candidate {
		return &Candidate{
			JobID: j.ID, Source: "slskd", Kind: "p2p", Username: "peer",
			Title: "offer", RemoteDir: "dir",
			Files: []CandidateFile{{Name: "dir/f.m4b"}},
		}
	}
	if err := st.SaveCandidate(mk()); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveCandidate(mk()); err != nil {
		t.Fatal(err)
	}
	cands, _ := st.Candidates(j.ID, 0)
	if len(cands) != 1 {
		t.Fatalf("candidates = %d, want 1 (duplicate ignored)", len(cands))
	}
}

func TestRejectConfirmGuards(t *testing.T) {
	st := openTest(t)
	j := mkJob("job-guards-44444444")
	if err := st.Create(j); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveCandidate(&Candidate{
		JobID: j.ID, Source: "slskd", Username: "u1", RemoteDir: "d1",
		Files: []CandidateFile{{Name: "d1/f.m4b"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveCandidate(&Candidate{
		JobID: j.ID, Source: "slskd", Username: "u2", RemoteDir: "d2",
		Files: []CandidateFile{{Name: "d2/f.m4b"}},
	}); err != nil {
		t.Fatal(err)
	}
	cands, _ := st.Candidates(j.ID, 0)
	if len(cands) != 2 {
		t.Fatalf("candidates = %d, want 2", len(cands))
	}
	// Reject one: it disappears from the offer list, count drops.
	if err := st.RejectCandidate(j.ID, cands[0].ID); err != nil {
		t.Fatal(err)
	}
	remain, _ := st.Candidates(j.ID, 0)
	if len(remain) != 1 {
		t.Fatalf("after reject = %d, want 1", len(remain))
	}
	if n, _ := st.CandidateCount(j.ID); n != 1 {
		t.Errorf("count = %d, want 1", n)
	}
	// Confirm the survivor with a subset.
	sel := []CandidateFile{{Name: "d2/f.m4b", Size: 5}}
	other := remain[0]
	if err := st.ConfirmCandidate(j.ID, other.ID, sel); err != nil {
		t.Fatal(err)
	}
	got, _ := st.CandidateByID(j.ID, other.ID)
	if got.ConfirmedAt.IsZero() {
		t.Error("confirmed_at not stamped")
	}
	if len(got.SelectedFiles) != 1 || got.SelectedFiles[0].Name != "d2/f.m4b" {
		t.Errorf("selected = %+v, want the subset", got.SelectedFiles)
	}
	// A confirmed-then-rejected row stays visible (history) but is not
	// re-confirmable and does not count as an offer.
	if err := st.RejectConfirmedCandidate(j.ID, other.ID); err != nil {
		t.Fatal(err)
	}
	vis, _ := st.Candidates(j.ID, 0)
	if len(vis) != 1 {
		t.Fatalf("visible after verify-reject = %d, want 1 (history row)", len(vis))
	}
	if n, _ := st.CandidateCount(j.ID); n != 0 {
		t.Errorf("count after verify-reject = %d, want 0", n)
	}
	if err := st.ConfirmCandidate(j.ID, other.ID, sel); err != ErrNotFound {
		t.Errorf("re-confirm rejected row = %v, want ErrNotFound", err)
	}
	// Rejecting an already-confirmed row via the confirm-screen path fails.
	if err := st.RejectCandidate(j.ID, other.ID); err != ErrNotFound {
		t.Errorf("reject confirmed row = %v, want ErrNotFound", err)
	}
}

func TestEvents(t *testing.T) {
	st := openTest(t)
	j := mkJob("job-events-55555555")
	if err := st.Create(j); err != nil {
		t.Fatal(err)
	}
	if err := st.AddEvent(j.ID, "search", "first"); err != nil {
		t.Fatal(err)
	}
	if err := st.AddEvent(j.ID, "grab", "second"); err != nil {
		t.Fatal(err)
	}
	evs, err := st.Events(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || evs[0].Message != "first" || evs[1].Message != "second" {
		t.Errorf("events = %+v, want ordered first,second", evs)
	}
}

func TestUpdateTags(t *testing.T) {
	st := openTest(t)
	j := mkJob("job-tags-66666666")
	if err := st.Create(j); err != nil {
		t.Fatal(err)
	}
	j.Series = "Lightbringer"
	j.SeriesSequence = "1"
	j.Description = "Blurb."
	j.Genre = "Fantasy"
	j.State = TagReview
	if err := st.UpdateTags(j); err != nil {
		t.Fatal(err)
	}
	got, _ := st.Get(j.ID)
	if got.Series != "Lightbringer" || got.SeriesSequence != "1" ||
		got.Description != "Blurb." || got.Genre != "Fantasy" {
		t.Errorf("tags not persisted: %+v", got)
	}
	// Plain Update still refuses to touch identity.
	j.Series = "Changed"
	if err := st.Update(j); err != nil {
		t.Fatal(err)
	}
	got, _ = st.Get(j.ID)
	if got.Series != "Lightbringer" {
		t.Errorf("Update mutated release fields: %+v", got)
	}
}
