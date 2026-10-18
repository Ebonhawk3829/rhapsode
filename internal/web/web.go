// Package web serves rhapsode's UI and API.
package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/Ebonhawk3829/rhapsode/internal/catalog"
	"github.com/Ebonhawk3829/rhapsode/internal/config"
	"github.com/Ebonhawk3829/rhapsode/internal/request"
	"github.com/Ebonhawk3829/rhapsode/internal/store"
)

// RequestCreator turns a release pick into a durable job. The interface keeps
// the web package free of the runner's dependencies.
type RequestCreator interface {
	// Request validates and stores a job for the given release.
	Request(rel request.Release) (*store.Job, error)
	// Confirm resumes a job in confirm with the user's file selection: an
	// empty selection takes everything the offer showed.
	Confirm(ctx context.Context, jobID, candidateID string, selected []store.CandidateFile) (*store.Job, error)
	// RejectCandidate records the user's rejection of one offer.
	RejectCandidate(jobID, candidateID string) (*store.Job, error)
	// Cancel abandons a job.
	Cancel(jobID string) (*store.Job, error)
}

// Server is the HTTP surface.
type Server struct {
	cfg      *config.File
	st       *store.Store
	cat      *catalog.Catalog
	requests RequestCreator
	http     *http.Server
}

// New builds the HTTP server.
func New(cfg *config.File, st *store.Store, cat *catalog.Catalog, rc RequestCreator) *Server {
	s := &Server{cfg: cfg, st: st, cat: cat, requests: rc}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /api/search", s.handleSearch)
	mux.HandleFunc("POST /api/jobs", s.handleCreateJob)
	mux.HandleFunc("POST /jobs", s.handleCreateJobForm)
	mux.HandleFunc("GET /jobs", s.handleJobsPage)
	mux.HandleFunc("GET /jobs/{id}", s.handleJobPage)
	mux.HandleFunc("POST /jobs/{id}/confirm", s.handleConfirmForm)
	mux.HandleFunc("POST /jobs/{id}/reject", s.handleRejectForm)
	mux.HandleFunc("POST /jobs/{id}/cancel", s.handleCancelForm)
	mux.HandleFunc("GET /api/jobs", s.handleListJobs)
	mux.HandleFunc("GET /api/jobs/{id}", s.handleGetJob)
	mux.HandleFunc("GET /api/jobs/{id}/candidates", s.handleListCandidates)
	mux.HandleFunc("POST /api/jobs/{id}/confirm", s.handleConfirmJob)
	mux.HandleFunc("POST /api/jobs/{id}/reject", s.handleRejectCandidate)
	mux.HandleFunc("POST /api/jobs/{id}/cancel", s.handleCancelJob)

	s.http = &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s
}

// ListenAndServe starts the server and blocks until it stops.
func (s *Server) ListenAndServe() error {
	return s.http.ListenAndServe()
}

// Shutdown stops the server, waiting for in-flight requests to finish.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}

// handleHealthz is deliberately shallow: it does not touch the database or the
// network, so a slow upstream cannot mark a correctly-running container
// unhealthy.
func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleSearch runs the omnisearch. A query that matches nothing is an empty
// list, not an error — the UI renders "no match".
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeError(w, http.StatusBadRequest, errors.New("q is required"))
		return
	}

	works, err := s.cat.Search(r.Context(), q)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if works == nil {
		works = []*catalog.Work{}
	}
	writeJSON(w, http.StatusOK, works)
}

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.st.List(store.State(r.URL.Query().Get("state")))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if jobs == nil {
		jobs = []*store.Job{}
	}
	writeJSON(w, http.StatusOK, jobs)
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	job, err := s.st.Get(id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("web: encode response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

// createJobBody is the JSON the UI submits for a request.
type createJobBody struct {
	Title          string `json:"title"`
	Author         string `json:"author"`
	Series         string `json:"series"`
	SeriesSequence string `json:"seriesSequence"`
	Narrator       string `json:"narrator"`
	Publisher      string `json:"publisher"`
	PublishedYear  string `json:"publishedYear"`
	IdentifierKind string `json:"identifierKind"`
	IdentifierVal  string `json:"identifierValue"`
	Query          string `json:"query"`
}

// handleCreateJob turns a release pick into a job and returns it with its
// ID and slug, which is what the UI shows while the runner takes over.
func (s *Server) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	var body createJobBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	rel := request.Release{
		Title:          body.Title,
		Author:         body.Author,
		Series:         body.Series,
		SeriesSequence: body.SeriesSequence,
		Narrator:       body.Narrator,
		Publisher:      body.Publisher,
		PublishedYear:  body.PublishedYear,
		Identifier: request.ID{
			Kind:  body.IdentifierKind,
			Value: body.IdentifierVal,
		},
		Query: body.Query,
	}

	job, err := s.requests.Request(rel)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, job)
}
