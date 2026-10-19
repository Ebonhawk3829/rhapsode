// Confirm flow: the job page's candidate list and the API behind it.
//
// The confirm screen is the intervention point between "searched" and
// "downloading". It lists the offers the search persisted, ordered by the
// scorer, each with its evidence lines; the user confirms one — naming
// files when the offer needs trimming — or rejects rows, or cancels the job.
package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/Ebonhawk3829/rhapsode/internal/store"
	"github.com/Ebonhawk3829/rhapsode/internal/tag"
)

// confirmFile is one selectable file on the confirm screen.
type confirmFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// confirmBody is the API payload of a confirm.
type confirmBody struct {
	CandidateID string        `json:"candidateId"`
	Files       []confirmFile `json:"files"` // empty = take the whole offer
}

// handleListCandidates serves a job's offers in confirm-screen shape: the
// winner-ordered list plus the evidence for each row.
func (s *Server) handleListCandidates(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	if _, err := s.st.Get(jobID); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	cands, err := s.st.Candidates(jobID, 0)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if cands == nil {
		cands = []*store.Candidate{}
	}
	writeJSON(w, http.StatusOK, cands)
}

// handleConfirmJob resumes a job with the user's pick.
func (s *Server) handleConfirmJob(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	var body confirmBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.CandidateID == "" {
		writeError(w, http.StatusBadRequest, errors.New("candidateId is required"))
		return
	}

	selected := make([]store.CandidateFile, 0, len(body.Files))
	for _, f := range body.Files {
		selected = append(selected, store.CandidateFile{Name: f.Name, Size: f.Size})
	}

	job, err := s.requests.Confirm(r.Context(), jobID, body.CandidateID, selected)
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

// handleRejectCandidate records a rejection; the job stays in confirm while
// anything remains on offer.
func (s *Server) handleRejectCandidate(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	var body struct {
		CandidateID string `json:"candidateId"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.CandidateID == "" {
		writeError(w, http.StatusBadRequest, errors.New("candidateId is required"))
		return
	}
	job, err := s.requests.RejectCandidate(jobID, body.CandidateID)
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.requests.Cancel(r.PathValue("id"))
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

// statusFor maps domain errors onto HTTP codes: a missing job or candidate
// is 404, a wrong-state transition is 409.
func statusFor(err error) int {
	if errors.Is(err, store.ErrNotFound) {
		return http.StatusNotFound
	}
	return http.StatusConflict
}

// jobPageHTML is the detail view for one job. For a job in confirm it is the
// decision screen; for every other state it is a status page.
var jobPageHTML = template.Must(template.New("job").Funcs(template.FuncMap{
	"startsWith": startsWith,
	"stateLabel": stateLabel,
	"Mul":        Mul,
}).Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
{{if .AutoRefresh}}<meta http-equiv="refresh" content="10">{{end}}
<title>{{.Job.Title}} — rhapsode</title>
<style>` + sharedCSS + `</style>
</head>
<body>
<div class="page">
<header class="topbar">
  <a class="brand" href="/">rhapsode</a>
  <nav><a href="/jobs">jobs</a></nav>
</header>
<h1>{{.Job.Title}}</h1>
<p class="sub">{{.Job.Author}}{{if .Job.Narrator}} &middot; read by {{.Job.Narrator}}{{end}}{{if .Job.Series}} &middot; {{.Job.Series}}{{if .Job.SeriesSequence}} #{{.Job.SeriesSequence}}{{end}}{{end}}</p>
<p>
  <span class="pill state-{{.Job.State}}">{{stateLabel .Job.State}}</span>
  <form class="inline" method="post" action="/jobs/{{.Job.ID}}/cancel"><button class="btn danger" type="submit">Cancel job</button></form>
</p>
{{if .Job.Error}}<div class="banner bad" role="alert">{{.Job.Error}}</div>{{end}}
{{if .TagReview}}
<form method="post" action="/jobs/{{.Job.ID}}/tags">
  <h2>Review tags</h2>
  <p class="sub">Proposed from the catalogue record. Edit before confirming — tone writes exactly this.</p>
  <div class="field"><label for="f-title">Title</label><input id="f-title" name="title" value="{{.Job.Title}}"></div>
  <div class="field"><label for="f-author">Author</label><input id="f-author" name="author" value="{{.Job.Author}}"></div>
  <div class="field"><label for="f-series">Series</label><input id="f-series" name="series" value="{{.Job.Series}}"></div>
  <div class="field"><label for="f-seq">Series sequence</label><input id="f-seq" name="seriesSequence" value="{{.Job.SeriesSequence}}"></div>
  <div class="field"><label for="f-narr">Narrator</label><input id="f-narr" name="narrator" value="{{.Job.Narrator}}"></div>
  <div class="field"><label for="f-pub">Publisher</label><input id="f-pub" name="publisher" value="{{.Job.Publisher}}"></div>
  <div class="field"><label for="f-year">Published year</label><input id="f-year" name="publishedYear" value="{{.Job.PublishedYear}}"></div>
  <div class="field"><label for="f-genre">Genre</label><input id="f-genre" name="genre" value="{{.Job.Genre}}"></div>
  <div class="field"><label for="f-desc">Description</label><textarea id="f-desc" name="description">{{.Job.Description}}</textarea></div>
  <div class="actions"><button class="btn primary" type="submit">Confirm tags and shelve</button></div>
</form>
{{else if .Confirming}}
  {{if .Sections}}
    <p class="sub">Pick the copy to download — or reject offers that are not the edition. Scores compare within a source, not across them.</p>
    {{range .Sections}}
    {{if .Candidates}}
    <h2>{{.Name}}{{if .EmptyNote}} <span class="meta">{{.EmptyNote}}</span>{{end}}</h2>
    {{range .Candidates}}
    <section class="card tone-{{.Band}}" aria-label="offer from {{if .Username}}{{.Username}}{{else}}unknown share{{end}}">
      <div class="title">{{if .Username}}{{.Username}}{{else}}unknown share{{end}} <span class="pill band-{{.Band}}">{{.Band}}</span></div>
      <div class="byline">{{.Format}}{{if .Bitrate}} &middot; {{.Bitrate}}kbps{{end}} &middot; {{.RenderSize}}</div>
      {{if .IsTorrent}}
      <ul class="reasons">
        {{range .Reasons}}<li class="{{if (startsWith . "✓")}}hit{{else if (startsWith . "✗")}}miss{{else}}note{{end}}">{{.}}</li>{{end}}
      </ul>
      {{else}}
      <ul class="reasons">
        {{range $i, $r := .Reasons}}{{if lt $i 3}}<li class="{{if (startsWith $r "✓")}}hit{{else if (startsWith $r "✗")}}miss{{else}}note{{end}}">{{$r}}</li>{{else}}<li class="evidence-extra {{if (startsWith $r "✓")}}hit{{else if (startsWith $r "✗")}}miss{{else}}note{{end}}">{{$r}}</li>{{end}}{{end}}
      </ul>
      {{if gt (len .Reasons) 3}}<button type="button" class="evidence-toggle" onclick="this.closest('.card').classList.toggle('show-all');this.textContent=this.closest('.card').classList.contains('show-all')?'show less':'show all evidence'">show all evidence</button>{{end}}
      {{end}}
      {{if .IsTorrent}}
      <p class="meta">Torrent — file list resolves at grab time.{{if .ExternalURL}} <a href="{{.ExternalURL}}" target="_blank" rel="noopener">View listing</a>{{end}}</p>
      <form method="post" action="/jobs/{{$.Job.ID}}/confirm">
        <div class="actions">
          <button class="btn primary" type="submit" name="candidateId" value="{{.ID}}">Download torrent</button>
      </form>
      <form method="post" action="/jobs/{{$.Job.ID}}/reject" class="inline">
        <button class="btn danger" type="submit" name="candidateId" value="{{.ID}}">Reject offer</button>
      </form>
        </div>
      {{else}}
      <form method="post" action="/jobs/{{$.Job.ID}}/confirm">
        <ul class="files">
          {{range .Files}}
          <li><label><input type="checkbox" name="file" value="{{.Name}}" checked> <code>{{.Name}}</code></label></li>
          {{end}}
        </ul>
        <div class="actions">
          <button class="btn primary" type="submit" name="candidateId" value="{{.ID}}">Download these files</button>
      </form>
      <form method="post" action="/jobs/{{$.Job.ID}}/reject" class="inline">
        <button class="btn danger" type="submit" name="candidateId" value="{{.ID}}">Reject offer</button>
      </form>
        </div>
      {{end}}
    </section>
    {{end}}
    {{end}}
    {{end}}
  {{else}}
  <p class="empty">No offers left. <a href="/jobs">Back to the queue</a>.</p>
  {{end}}
{{else}}
  <div class="card">
    <div class="title">Progress {{printf "%.0f" (Mul .Job.Progress 100)}}%</div>
    <div class="bar" style="margin:.5rem 0"><div style="width:{{printf "%.0f" (Mul .Job.Progress 100)}}%"></div></div>
    {{if .GrabID}}<p class="meta">Downloading via {{.Grabber}} ({{.GrabID}})</p>{{end}}
  </div>
{{end}}
</div>
</body>
</html>`))

// jobView is the template's data.
type jobView struct {
	Job        *store.Job
	Confirming bool
	TagReview  bool
	// AutoRefresh reloads the page every 10s on progress states
	// (downloading, verifying, merging, tagging, shelving) where there is
	// no form state to lose. Decision states (confirm, tag-review) never
	// auto-reload: a refresh mid-edit would wipe checkbox and field input.
	AutoRefresh bool
	Sections    []*sourceSection
	Grabber     string
	GrabID      string
}

// sourceSection is one source's part of the confirm screen. The two sources
// rank on different currencies — slskd scores peer-typed share text, ABB
// grades catalogue metadata — so a single merged list would fake a
// comparability that does not exist. One section per source keeps the
// rankings honest and degrades gracefully: an empty section renders nothing.
type sourceSection struct {
	Name       string
	EmptyNote  string
	Candidates []*candidateView
}

// sectionFor groups candidates by source name for display.
func sectionFor(name string) *sourceSection {
	return &sourceSection{Name: name}
}

// candidateView is one row on the confirm screen, pre-shaped for HTML.
type candidateView struct {
	ID          string
	Username    string
	Format      string
	Bitrate     int
	RenderSize  string
	Band        string
	ExternalURL string
	// IsTorrent marks magnet offers: no file list exists pre-grab, so the
	// row renders without checkboxes and the detail path never shows as a
	// "file". The path is transport plumbing, not a download location.
	IsTorrent bool
	Reasons   []string
	Files     []store.CandidateFile
}

// handleJobPage renders one job: the candidate decision list when it is in
// confirm, plain status otherwise.
//
// A GET form per candidate keeps this no-JavaScript like the rest of the UI:
// confirming with a trimmed file set is a checkbox list inside one form, and
// rejecting is a second submit target via formaction.
func (s *Server) handleJobPage(w http.ResponseWriter, r *http.Request) {
	job, err := s.st.Get(r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	v := jobView{Job: job}
	switch job.State {
	case store.Confirm:
		v.Confirming = true
		cands, err := s.st.Candidates(job.ID, 0)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		// Known sources render in a stable order; unknown ones append so a
		// new source never disappears behind a renamed section.
		bySrc := map[string]*sourceSection{}
		var order []*sourceSection
		for _, name := range []string{"slskd", "audiobookbay"} {
			bySrc[name] = sectionFor(name)
			order = append(order, bySrc[name])
		}
		for _, c := range cands {
			sec, ok := bySrc[c.Source]
			if !ok {
				sec = sectionFor(c.Source)
				bySrc[c.Source] = sec
				order = append(order, sec)
			}
			sec.Candidates = append(sec.Candidates, &candidateView{
				ID:          c.ID,
				Username:    c.Username,
				Format:      c.Format,
				Bitrate:     c.Bitrate,
				RenderSize:  renderSize(c.SizeBytes),
				Band:        c.Band,
				IsTorrent:   c.Kind == "torrent",
				ExternalURL: c.ExternalURL,
				Reasons:     c.Reasons,
				Files:       c.Files,
			})
		}
		for _, sec := range order {
			if len(sec.Candidates) == 0 {
				// An empty section says so rather than vanishing: the user
				// should know both sources were asked.
				sec.EmptyNote = "nothing found"
				v.Sections = append(v.Sections, sec)
			} else {
				v.Sections = append(v.Sections, sec)
			}
		}
	case store.TagReview:
		v.TagReview = true
	case store.Downloading, store.Verifying, store.Merging, store.Tagging, store.Shelving:
		v.AutoRefresh = true
		v.Grabber = job.Grabber
		v.GrabID = job.GrabID
	default:
		v.Grabber = job.Grabber
		v.GrabID = job.GrabID
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := jobPageHTML.Execute(w, v); err != nil {
		s.logf("web: render job page: %v", err)
	}
}

// renderSize makes a byte count human for the confirm screen.
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

// handleConfirmForm accepts the job page's HTML form (checkbox file list).
func (s *Server) handleConfirmForm(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	candidateID := r.PostFormValue("candidateId")
	if candidateID == "" {
		writeError(w, http.StatusBadRequest, errors.New("candidateId is required"))
		return
	}

	// Checked boxes are the selection; none checked means the whole offer.
	// Sizes are restored from the stored candidate: the form posts names
	// only, and the slskd enqueue replays names with sizes.
	names := r.Form["file"]
	selected := make([]store.CandidateFile, 0, len(names))
	if len(names) > 0 {
		c, err := s.st.CandidateByID(jobID, candidateID)
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		byName := make(map[string]int64, len(c.Files))
		for _, f := range c.Files {
			byName[f.Name] = f.Size
		}
		for _, name := range names {
			selected = append(selected, store.CandidateFile{Name: name, Size: byName[name]})
		}
	}
	if _, err := s.requests.Confirm(r.Context(), jobID, candidateID, selected); err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	http.Redirect(w, r, "/jobs/"+jobID, http.StatusSeeOther)
}

// handleTagsForm accepts the tag-review form: the user's edited proposal.
func (s *Server) handleTagsForm(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	m := tag.Metadata{
		Title:          r.PostFormValue("title"),
		Author:         r.PostFormValue("author"),
		Series:         r.PostFormValue("series"),
		SeriesSequence: r.PostFormValue("seriesSequence"),
		Narrator:       r.PostFormValue("narrator"),
		Publisher:      r.PostFormValue("publisher"),
		PublishedYear:  r.PostFormValue("publishedYear"),
		Description:    r.PostFormValue("description"),
		Genre:          r.PostFormValue("genre"),
	}
	if _, err := s.requests.ConfirmTags(r.Context(), jobID, m); err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	http.Redirect(w, r, "/jobs/"+jobID, http.StatusSeeOther)
}

// handleConfirmTags is the JSON variant of the tag-review confirm.
func (s *Server) handleConfirmTags(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	var body struct {
		Title          string `json:"title"`
		Author         string `json:"author"`
		Series         string `json:"series"`
		SeriesSequence string `json:"seriesSequence"`
		Narrator       string `json:"narrator"`
		Publisher      string `json:"publisher"`
		PublishedYear  string `json:"publishedYear"`
		Description    string `json:"description"`
		Genre          string `json:"genre"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	job, err := s.requests.ConfirmTags(r.Context(), jobID, tag.Metadata(body))
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

// handleRejectForm accepts the reject button's formaction target.
func (s *Server) handleRejectForm(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	candidateID := r.PostFormValue("candidateId")
	if candidateID == "" {
		writeError(w, http.StatusBadRequest, errors.New("candidateId is required"))
		return
	}
	if _, err := s.requests.RejectCandidate(jobID, candidateID); err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	http.Redirect(w, r, "/jobs/"+jobID, http.StatusSeeOther)
}

// handleCancelForm accepts the job page's cancel button.
func (s *Server) handleCancelForm(w http.ResponseWriter, r *http.Request) {
	if _, err := s.requests.Cancel(r.PathValue("id")); err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	http.Redirect(w, r, "/jobs", http.StatusSeeOther)
}

// handleClearForm removes a terminal job from the queue.
func (s *Server) handleClearForm(w http.ResponseWriter, r *http.Request) {
	if err := s.requests.Clear(r.PathValue("id")); err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	http.Redirect(w, r, "/jobs", http.StatusSeeOther)
}

// handleClearJob is the JSON variant.
func (s *Server) handleClearJob(w http.ResponseWriter, r *http.Request) {
	if err := s.requests.Clear(r.PathValue("id")); err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cleared"})
}

// logf is a short alias so handlers do not reach for the log package.
func (s *Server) logf(format string, args ...any) { log.Printf(format, args...) }

// startsWith is a template helper: the reasons list mixes hits ("✓ …") and
// misses ("✗ …"), and the classes colour them apart.
func startsWith(s, prefix string) bool { return strings.HasPrefix(s, prefix) }

// Mul is a template helper for the progress percentage.
func Mul(a, b float64) float64 { return a * b }
