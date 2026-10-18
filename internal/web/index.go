package web

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strings"

	"github.com/Ebonhawk3829/rhapsode/internal/catalog"
	"github.com/Ebonhawk3829/rhapsode/internal/request"
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
<style>
  body { font-family: system-ui, sans-serif; margin: 2rem auto; max-width: 720px; padding: 0 1rem; color: #222; }
  h1 { font-size: 1.4rem; }
  form.search { display: flex; gap: .5rem; margin: 1rem 0 2rem; }
  form.search input { flex: 1; font-size: 1rem; padding: .5rem; }
  .work { border: 1px solid #ddd; border-radius: 6px; padding: .75rem 1rem; margin-bottom: .75rem; }
  .work h2 { font-size: 1.05rem; margin: 0 0 .25rem; }
  .work .meta { color: #666; font-size: .9rem; }
  .releases { margin: .5rem 0 0; padding: 0; list-style: none; }
  form.inline { display: inline; margin: 0; }
  .releases li { display: flex; justify-content: space-between; align-items: baseline; padding: .25rem 0; border-top: 1px dotted #eee; }
  .releases .id { font-family: monospace; font-size: .8rem; color: #888; }
  button { font-size: .85rem; padding: .25rem .75rem; cursor: pointer; }
  .empty { color: #888; font-style: italic; }
</style>
</head>
<body>
<h1>rhapsode</h1>
<form class="search" method="get" action="/">
  <input type="search" name="q" placeholder="Search for an audiobook&hellip;" value="{{.Query}}" autofocus>
  <button type="submit">Search</button>
</form>
{{if .Query}}
  {{if .Works}}
    {{range .Works}}
    <div class="work">
      <h2>{{.Title}}</h2>
      <div class="meta">{{.Author}}{{if .Series}} &middot; {{.Series}}{{end}}</div>
      <ul class="releases">
        {{range .Releases}}
        <li>
          <span>{{.Narrator}}{{if .Publisher}} &middot; {{.Publisher}}{{end}}{{if .PublishedYear}} &middot; {{.PublishedYear}}{{end}} <span class="id">{{.Identifier.Kind}}:{{.Identifier.Value}}</span></span>
          <form method="post" action="/jobs" class="inline">
            <input type="hidden" name="title" value="{{.WorkTitle}}">
            <input type="hidden" name="author" value="{{.WorkAuthor}}">
            <input type="hidden" name="series" value="{{.WorkSeries}}">
            <input type="hidden" name="narrator" value="{{.Narrator}}">
            <input type="hidden" name="publisher" value="{{.Publisher}}">
            <input type="hidden" name="publishedYear" value="{{.PublishedYear}}">
            <input type="hidden" name="identifierKind" value="{{.Identifier.Kind}}">
            <input type="hidden" name="identifierValue" value="{{.Identifier.Value}}">
            <input type="hidden" name="query" value="{{.Query}}">
            <button type="submit">Request</button>
          </form>
        </li>
        {{end}}
      </ul>
    </div>
    {{end}}
  {{else}}
    <p class="empty">No match for &ldquo;{{.Query}}&rdquo;.</p>
  {{end}}
{{end}}
</body>
</html>`))

// indexView is what the template renders.
type indexView struct {
	Query string
	Works []*catalogWorkView
}

// catalogReleaseView is one release row in the HTML: the release fields plus
// the work context the request form needs to submit a complete job.
type catalogReleaseView struct {
	*catalog.Release
	WorkTitle  string
	WorkAuthor string
	WorkSeries string
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
	v := indexView{Query: q}

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
				v.Works[len(v.Works)-1].Releases = append(
					v.Works[len(v.Works)-1].Releases,
					&catalogReleaseView{
						Release:    r,
						WorkTitle:  wk.Title,
						WorkAuthor: wk.Author,
						WorkSeries: wk.Series,
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
	// Back to the list of current jobs: the user sees their request queueing.
	http.Redirect(w, r, "/jobs", http.StatusSeeOther)
	_ = job
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
	b.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8"><title>rhapsode — jobs</title>`)
	b.WriteString(`<style>body{font-family:system-ui,sans-serif;margin:2rem auto;max-width:720px;padding:0 1rem;}
table{border-collapse:collapse;width:100%;}
td,th{border-bottom:1px solid #eee;padding:.4rem .5rem;text-align:left;font-size:.9rem;}
.state-Done{color:#070;}.state-Failed{color:#c00;}.state-Cancelled{color:#888;}
.bar{background:#eee;border-radius:3px;height:6px;width:100%;overflow:hidden;}
.bar>div{background:#36c;height:6px;}</style></head><body>`)
	b.WriteString(`<h1><a href="/" style="text-decoration:none;color:inherit">rhapsode</a> &middot; jobs</h1>`)
	b.WriteString(`<table><tr><th>Title</th><th>State</th><th>Progress</th><th>Source</th></tr>`)
	if jobs == nil {
		b.WriteString(`<tr><td colspan="4" style="color:#888;font-style:italic">No jobs.</td></tr>`)
	}
	for _, j := range jobs {
		bar := ""
		if j.Progress > 0 {
			bar = fmt.Sprintf(`<div class="bar"><div style="width:%.0f%%"></div></div>`, j.Progress*100)
		}
		b.WriteString(`<tr><td>` + template.HTMLEscapeString(j.Title) + `</td>`)
		b.WriteString(`<td class="state-` + template.HTMLEscapeString(string(j.State)) + `">` + template.HTMLEscapeString(string(j.State)) + `</td>`)
		b.WriteString(`<td>` + bar + fmt.Sprintf("%.0f%%", j.Progress*100) + `</td>`)
		b.WriteString(`<td>` + template.HTMLEscapeString(j.Source) + `</td></tr>`)
	}
	b.WriteString(`</table></body></html>`)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}
