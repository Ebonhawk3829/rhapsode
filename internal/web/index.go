package web

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strings"

	"github.com/Ebonhawk3829/rhapsode/internal/catalog"
	"github.com/Ebonhawk3829/rhapsode/internal/request"
	"github.com/Ebonhawk3829/rhapsode/internal/store"
)

// indexHTML is the whole UI.
//
// rhapsode ships no JavaScript framework: the flow is a search box, a list of
// works, a releases panel and a request button, and plain HTML with a form
// per step covers it. The page reloads between steps; when a step needs to
// feel instant it can become a fetch call without changing the server side,
// because the handlers already speak JSON.
var indexHTML = template.Must(template.New("index").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>rhapsode</title>
<style>` + sharedCSS + `</style>
</head>
<body>
<div class="page">
<header class="topbar">
  <a class="brand" href="/">rhapsode</a>
  {{if .ShowJobsNav}}<nav><a href="/jobs">jobs</a></nav>{{end}}
</header>
{{if .Degraded}}
<div class="banner warn" role="status">Degraded: {{range $i, $d := .Degraded}}{{if $i}}, {{end}}{{$d}}{{end}} unconfigured — results will be thin.</div>
{{end}}
<form class="search" method="get" action="/">
  <input type="search" name="q" placeholder="Author, series, title, ISBN, ASIN…" value="{{.Query}}" autofocus aria-label="Search for an audiobook">
  <button class="btn primary" type="submit">Search</button>
</form>
{{if .Query}}
  {{if .Works}}
    <p class="sub">{{len .Works}} work{{if gt (len .Works) 1}}s{{end}} for &ldquo;{{.Query}}&rdquo; — pick the edition you want.</p>
    {{range .Works}}
    <section class="card" aria-label="{{.Title}}">
      <div class="title">{{.Title}}</div>
      <div class="byline">{{.Author}}{{if .Series}} &middot; {{.Series}}{{end}}</div>
      {{range .Releases}}
      <div class="release">
        <span>{{.Narrator}}{{if .Publisher}} &middot; {{.Publisher}}{{end}}{{if .PublishedYear}} &middot; {{.PublishedYear}}{{end}} <span class="id">{{.Identifier.Kind}}:{{.Identifier.Value}}</span></span>
        <form method="post" action="/jobs" class="inline">
          <input type="hidden" name="title" value="{{.WorkTitle}}">
          <input type="hidden" name="author" value="{{.WorkAuthor}}">
          <input type="hidden" name="series" value="{{.Series}}">
          <input type="hidden" name="seriesSequence" value="{{.SeriesSequence}}">
          <input type="hidden" name="narrator" value="{{.Narrator}}">
          <input type="hidden" name="publisher" value="{{.Publisher}}">
          <input type="hidden" name="publishedYear" value="{{.PublishedYear}}">
          <input type="hidden" name="description" value="{{.Description}}">
          <input type="hidden" name="genre" value="{{.Genre}}">
          <input type="hidden" name="identifierKind" value="{{.Identifier.Kind}}">
          <input type="hidden" name="identifierValue" value="{{.Identifier.Value}}">
          <input type="hidden" name="query" value="{{.Query}}">
          <button class="btn primary" type="submit">Request</button>
        </form>
      </div>
      {{end}}
    </section>
    {{end}}
  {{else}}
    <p class="empty">No match for &ldquo;{{.Query}}&rdquo;.</p>
    <form method="post" action="/jobs" class="inline">
      <input type="hidden" name="title" value="{{.Query}}">
      <input type="hidden" name="query" value="{{.Query}}">
      <button class="btn" type="submit">Request anyway with this query</button>
    </form>
    <p class="meta">The sources will be searched on the raw text; no catalogue metadata will pre-fill the tags.</p>
  {{end}}
{{else}}
  <p class="sub">Search the catalogues, confirm the edition, pick the copy. Downloads, verification, tagging and shelving run from the job page.</p>
{{end}}
</div>
</body>
</html>`))

// indexView is what the template renders.
type indexView struct {
	Query string
	Works []*catalogWorkView
	// Degraded names the pieces running below full strength: providers or
	// sources left unconfigured. Blank details mean off, and off means the
	// UI says so instead of silently returning thin results.
	Degraded []string
	// ShowJobsNav renders the jobs link. The jobs page itself omits it: a
	// link to the page you are on is noise.
	ShowJobsNav bool
}

// catalogReleaseView is one release row in the HTML: the release fields plus
// the work context the request form needs to submit a complete job.
type catalogReleaseView struct {
	*catalog.Release
	WorkTitle  string
	WorkAuthor string
	// Genre flattens the release's genre list for the hidden form field.
	Genre string
	// Query is the raw search text, kept so catalogue-less requests still
	// carry something to search sources with.
	Query string
}

// catalogWorkView is the JSON shape of a Work, reused for HTML rendering so
// the API and the page cannot drift apart.
type catalogWorkView struct {
	Title    string
	Author   string
	Series   string
	Releases []*catalogReleaseView
}

// handleIndex serves the omnisearch page.
//
// The page is a GET form, so a search is a URL: /?q=black+prism is a
// bookmarkable search result, and the browser back button behaves the way the
// user expects.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	q := strings.TrimSpace(r.URL.Query().Get("q"))
	v := indexView{Query: q, Degraded: s.degraded(), ShowJobsNav: true}

	if q != "" {
		works, err := s.cat.Search(r.Context(), q)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		for _, wk := range works {
			if len(wk.Releases) == 0 {
				continue
			}
			v.Works = append(v.Works, &catalogWorkView{
				Title:  wk.Title,
				Author: wk.Author,
				Series: wk.Series,
			})
			for _, r := range wk.Releases {
				genre := ""
				if len(r.Genres) > 0 {
					genre = r.Genres[0]
				}
				v.Works[len(v.Works)-1].Releases = append(
					v.Works[len(v.Works)-1].Releases,
					&catalogReleaseView{
						Release:    r,
						WorkTitle:  wk.Title,
						WorkAuthor: wk.Author,
						Genre:      genre,
						Query:      q,
					},
				)
			}
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := indexHTML.Execute(w, v); err != nil {
		log.Printf("web: render index: %v", err)
	}
}

// handleCreateJobForm accepts the HTML form the Request buttons submit.
//
// It posts to its own handler rather than /api/jobs because a form cannot
// speak JSON; the API route stays for programmatic clients. Both funnel into
// the same Creator, so validation and slugging cannot diverge.
func (s *Server) handleCreateJobForm(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	rel := request.Release{
		Title:          r.PostFormValue("title"),
		Author:         r.PostFormValue("author"),
		Series:         r.PostFormValue("series"),
		SeriesSequence: r.PostFormValue("seriesSequence"),
		Narrator:       r.PostFormValue("narrator"),
		Publisher:      r.PostFormValue("publisher"),
		PublishedYear:  r.PostFormValue("publishedYear"),
		Description:    r.PostFormValue("description"),
		Genre:          r.PostFormValue("genre"),
		Identifier: request.ID{
			Kind:  r.PostFormValue("identifierKind"),
			Value: r.PostFormValue("identifierValue"),
		},
		Query: r.PostFormValue("query"),
	}

	job, err := s.requests.Request(rel)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	// Land on the new job's page: the confirm screen is where the request
	// goes next, and the jobs list links every row there too.
	http.Redirect(w, r, "/jobs/"+job.ID, http.StatusSeeOther)
}

// stateLabel renders a job state as a human action line: the pill should
// say what the job needs, not the machine's internal name for it. Labels
// use normal title case — lowercase pills read as code, not status.
func stateLabel(s store.State) string {
	switch s {
	case store.Pending:
		return "Queued"
	case store.Searching:
		return "Searching"
	case store.Confirm:
		return "Pick a Copy"
	case store.Downloading:
		return "Downloading"
	case store.Verifying:
		return "Verifying"
	case store.Merging:
		return "Merging"
	case store.TagReview:
		return "Review Tags"
	case store.Tagging:
		return "Tagging"
	case store.Shelving:
		return "Shelving"
	case store.Done:
		return "Shelved"
	case store.Failed:
		return "Failed"
	case store.Cancelled:
		return "Cancelled"
	default:
		return string(s)
	}
}

// handleJobsPage lists the current job queue. The redirect target of a
// accepted request, and where you look when something is stuck.
func (s *Server) handleJobsPage(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.st.List("")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>rhapsode · jobs</title><style>`)
	b.WriteString(sharedCSS)
	b.WriteString(`</style></head><body><div class="page">`)
	b.WriteString(`<header class="topbar"><a class="brand" href="/">rhapsode</a></header>`)
	b.WriteString(`<h1>Jobs</h1>`)
	if jobs == nil {
		b.WriteString(`<p class="empty">No jobs yet — <a href="/">search the catalogues</a> to start one.</p>`)
	} else {
		visible := 0
		for _, j := range jobs {
			if j.State != store.Done {
				visible++
			}
		}
		if visible == 0 {
			b.WriteString(`<p class="empty">Everything shelved — <a href="/">search the catalogues</a> for the next one.</p>`)
		} else {
			b.WriteString(`<table class="jobs"><tr><th>Title</th><th>State</th><th>Progress</th><th></th></tr>`)
			for _, j := range jobs {
				if j.State == store.Done {
					continue
				}
				bar := ""
				if j.Progress > 0 {
					bar = fmt.Sprintf(`<div class="bar"><div style="width:%.0f%%"></div></div>`, j.Progress*100)
				}
				b.WriteString(`<tr><td><a class="rowlink" href="/jobs/` + template.HTMLEscapeString(j.ID) + `">` + template.HTMLEscapeString(j.Title) + `</a>`)
				if j.Author != "" {
					b.WriteString(`<div class="meta">` + template.HTMLEscapeString(j.Author) + `</div>`)
				}
				b.WriteString(`</td>`)
				b.WriteString(`<td><span class="pill state-` + template.HTMLEscapeString(string(j.State)) + `">` + template.HTMLEscapeString(stateLabel(j.State)) + `</span></td>`)
				b.WriteString(`<td>` + bar + fmt.Sprintf("%.0f%%", j.Progress*100) + `</td>`)
				if j.State.Terminal() {
					b.WriteString(`<td><form class="inline" method="post" action="/jobs/` + template.HTMLEscapeString(j.ID) + `/clear"><button class="btn" type="submit">Clear</button></form></td>`)
				} else {
					b.WriteString(`<td></td>`)
				}
				b.WriteString(`</tr>`)
			}
			b.WriteString(`</table>`)
		}
	}
	b.WriteString(`</div></body></html>`)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}
