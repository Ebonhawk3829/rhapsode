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
		writeError(w, http.StatusConflict, err)
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
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.requests.Cancel(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

// jobPageHTML is the detail view for one job. For a job in confirm it is the
// decision screen; for every other state it is a status page.
var jobPageHTML = template.Must(template.New("job").Funcs(template.FuncMap{
	"startsWith": startsWith,
	"Mul":        Mul,
}).Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Job.Title}} — rhapsode</title>
<style>
  body { font-family: system-ui, sans-serif; margin: 2rem auto; max-width: 860px; padding: 0 1rem; color: #222; }
  h1 { font-size: 1.3rem; }
  .meta { color: #666; font-size: .9rem; margin-bottom: 1rem; }
  .candidate { border: 1px solid #ddd; border-radius: 6px; padding: .75rem 1rem; margin-bottom: .75rem; }
  .candidate.band-accept { border-color: #4a4; }
  .candidate.band-reject { border-color: #d99; background: #fffaf8; }
  .reasons { color: #555; font-size: .85rem; margin: .25rem 0; }
  .reasons span { display: inline-block; margin-right: .75rem; }
  .hit { color: #262; } .miss { color: #a33; }
  ul.files { margin: .25rem 0; padding-left: 1.2rem; font-size: .85rem; font-family: monospace; }
  form.inline { display: inline-block; margin: 0; }
  button { font-size: .85rem; padding: .25rem .75rem; cursor: pointer; }
  button.danger { color: #a11; }
  .state { display: inline-block; padding: .1rem .5rem; border-radius: 4px; background: #eee; font-size: .85rem; }
  .progressbar { background:#eee; border-radius:3px; height:6px; width:100%; overflow:hidden; margin:.4rem 0; }
  .progressbar>div { background:#36c; height:6px; }
</style>
</head>
<body>
<h1><a href="/jobs" style="text-decoration:none;color:inherit">&larr;</a> {{.Job.Title}}</h1>
<div class="meta">{{.Job.Author}}{{if .Job.Narrator}} &middot; {{.Job.Narrator}}{{end}}
 &middot; state <span class="state">{{.Job.State}}</span>
{{if .Job.Error}} &middot; error: {{.Job.Error}}{{end}}
 &middot; <form class="inline" method="post" action="/jobs/{{.Job.ID}}/cancel"><button class="danger" type="submit">Cancel job</button></form>
</div>
{{if .Confirming}}
  {{if .Sections}}
  <form method="post" action="/jobs/{{.Job.ID}}/confirm">
    {{range .Sections}}
    {{if .Candidates}}
    <h2>{{.Name}}{{if .EmptyNote}} <small class="meta">{{.EmptyNote}}</small>{{end}}</h2>
    {{range .Candidates}}
    <div class="candidate band-{{.Band}}">
      <strong>{{if .Username}}{{.Username}}{{else}}unknown share{{end}}</strong>
      &middot; {{.Format}}{{if .Bitrate}} {{.Bitrate}}kbps{{end}}
      &middot; {{.RenderSize}}
      <div class="reasons">
        {{range .Reasons}}<span class="{{if (startsWith . "✓")}}hit{{else}}miss{{end}}">{{.}}</span>{{end}}
      </div>
      <ul class="files">
        {{range .Files}}
        <li><label><input type="checkbox" name="file" value="{{.Name}}" checked> {{.Name}}</label></li>
        {{end}}
      </ul>
      <button type="submit" name="candidateId" value="{{.ID}}">Download these files</button>
      <button type="submit" name="candidateId" value="{{.ID}}" formaction="/jobs/{{$.Job.ID}}/reject" class="danger">Reject offer</button>
    </div>
    {{end}}
    {{end}}
    {{end}}
  </form>
  {{else}}
  <p class="meta">No offers left. <a href="/jobs">Back to the queue</a>.</p>
  {{end}}
{{else}}
  <div class="meta">Progress {{printf "%.0f" (Mul .Job.Progress 100)}}%</div>
  <div class="progressbar"><div style="width:{{printf "%.0f" (Mul .Job.Progress 100)}}%"></div></div>
  {{if .GrabID}}<p class="meta">Downloading via {{.Grabber}} ({{.GrabID}})</p>{{end}}
{{end}}
</body>
</html>`))

// jobView is the template's data.
type jobView struct {
	Job        *store.Job
	Confirming bool
	Sections   []*sourceSection
	Grabber    string
	GrabID     string
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
	ID         string
	Username   string
	Format     string
	Bitrate    int
	RenderSize string
	Band       string
	Reasons    []string
	Files      []store.CandidateFile
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
	if job.State == store.Confirm {
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
				ID:         c.ID,
				Username:   c.Username,
				Format:     c.Format,
				Bitrate:    c.Bitrate,
				RenderSize: renderSize(c.SizeBytes),
				Band:       c.Band,
				Reasons:    c.Reasons,
				Files:      c.Files,
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
	} else {
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
	names := r.Form["file"]
	selected := make([]store.CandidateFile, 0, len(names))
	for _, name := range names {
		selected = append(selected, store.CandidateFile{Name: name})
	}
	if _, err := s.requests.Confirm(r.Context(), jobID, candidateID, selected); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	http.Redirect(w, r, "/jobs/"+jobID, http.StatusSeeOther)
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
		writeError(w, http.StatusConflict, err)
		return
	}
	http.Redirect(w, r, "/jobs/"+jobID, http.StatusSeeOther)
}

// handleCancelForm accepts the job page's cancel button.
func (s *Server) handleCancelForm(w http.ResponseWriter, r *http.Request) {
	if _, err := s.requests.Cancel(r.PathValue("id")); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	http.Redirect(w, r, "/jobs", http.StatusSeeOther)
}

// logf is a short alias so handlers do not reach for the log package.
func (s *Server) logf(format string, args ...any) { log.Printf(format, args...) }

// startsWith is a template helper: the reasons list mixes hits ("✓ …") and
// misses ("✗ …"), and the classes colour them apart.
func startsWith(s, prefix string) bool { return strings.HasPrefix(s, prefix) }

// Mul is a template helper for the progress percentage.
func Mul(a, b float64) float64 { return a * b }
