package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Ebonhawk3829/rhapsode/internal/catalog"
	"github.com/Ebonhawk3829/rhapsode/internal/config"
	"github.com/Ebonhawk3829/rhapsode/internal/request"
	"github.com/Ebonhawk3829/rhapsode/internal/store"
	"github.com/Ebonhawk3829/rhapsode/internal/tag"
)

type stubRequests struct {
	confirmSel []store.CandidateFile
	confirmErr error
	rejectErr  error
	cancelErr  error
}

func (s *stubRequests) Request(rel request.Release) (*store.Job, error) {
	return &store.Job{ID: "job-1", Title: rel.Title, Slug: "slug"}, nil
}
func (s *stubRequests) Confirm(_ context.Context, _, _ string, sel []store.CandidateFile) (*store.Job, error) {
	s.confirmSel = sel
	return &store.Job{ID: "job-1"}, s.confirmErr
}
func (s *stubRequests) RejectCandidate(_, _ string) (*store.Job, error) {
	return &store.Job{ID: "job-1"}, s.rejectErr
}
func (s *stubRequests) ConfirmTags(_ context.Context, _ string, _ tag.Metadata) (*store.Job, error) {
	return &store.Job{ID: "job-1"}, nil
}
func (s *stubRequests) Cancel(_ string) (*store.Job, error) {
	return &store.Job{ID: "job-1"}, s.cancelErr
}
func (s *stubRequests) Clear(string) error { return nil }

func newTestServer(t *testing.T) (*Server, *store.Store, *stubRequests) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cfg := &config.File{}
	cfg.Server.Listen = "127.0.0.1:0"
	reqs := &stubRequests{}
	return New(cfg, st, catalog.New(), reqs), st, reqs
}

func TestConfirmFormScopesFilesToCandidate(t *testing.T) {
	srv, st, reqs := newTestServer(t)
	j := &store.Job{Title: "T", Slug: "slug-1", State: store.Confirm}
	if err := st.Create(j); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ user, dir string }{{"peer-a", "dir-a"}, {"peer-b", "dir-b"}} {
		if err := st.SaveCandidate(&store.Candidate{
			JobID: j.ID, Source: "slskd", Username: c.user, RemoteDir: c.dir,
			Files: []store.CandidateFile{{Name: c.dir + "/f.m4b", Size: 7}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	cands, _ := st.Candidates(j.ID, 0)
	if len(cands) != 2 {
		t.Fatalf("seeded %d candidates", len(cands))
	}
	// Submit the form for the second candidate only, with its own checkbox.
	form := url.Values{}
	form.Set("candidateId", cands[1].ID)
	form.Add("file", "dir-b/f.m4b")
	r := httptest.NewRequest(http.MethodPost, "/jobs/"+j.ID+"/confirm", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetPathValue("id", j.ID)
	w := httptest.NewRecorder()
	srv.handleConfirmForm(w, r)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", w.Code)
	}
	if len(reqs.confirmSel) != 1 || reqs.confirmSel[0].Name != "dir-b/f.m4b" {
		t.Errorf("selection = %+v, want only dir-b's file", reqs.confirmSel)
	}
}

func TestConfirmFormMissingCandidateID(t *testing.T) {
	srv, st, _ := newTestServer(t)
	j := &store.Job{Title: "T", Slug: "slug-1", State: store.Confirm}
	if err := st.Create(j); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/jobs/"+j.ID+"/confirm", strings.NewReader(""))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetPathValue("id", j.ID)
	w := httptest.NewRecorder()
	srv.handleConfirmForm(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestSearchRequiresQuery(t *testing.T) {
	srv, _, _ := newTestServer(t)
	r := httptest.NewRequest(http.MethodGet, "/api/search", nil)
	w := httptest.NewRecorder()
	srv.handleSearch(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestTagsFormReviews(t *testing.T) {
	srv, st, _ := newTestServer(t)
	j := &store.Job{Title: "T", Slug: "slug-1", State: store.TagReview, Phase: "/tmp/x.m4b"}
	if err := st.Create(j); err != nil {
		t.Fatal(err)
	}
	form := url.Values{}
	form.Set("title", "Edited Title")
	form.Set("author", "Author")
	r := httptest.NewRequest(http.MethodPost, "/jobs/"+j.ID+"/tags", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetPathValue("id", j.ID)
	w := httptest.NewRecorder()
	srv.handleTagsForm(w, r)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", w.Code)
	}
}

func TestFaviconServesIcon(t *testing.T) {
	srv, _, _ := newTestServer(t)
	r := httptest.NewRequest(http.MethodGet, "/favicon.ico", nil)
	w := httptest.NewRecorder()
	srv.handleFavicon(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("content-type = %q, want image/png", ct)
	}
	if w.Body.Len() == 0 {
		t.Error("empty favicon body")
	}
}
