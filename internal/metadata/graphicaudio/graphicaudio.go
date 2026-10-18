// Package graphicaudio implements metadata.Provider against the self-hosted
// GraphicAudio scraper.
//
// GA releases are full-cast dramatisations carrying ISBNs. Search is the fuzzy
// entry point behind the omnisearch; Lookup enriches a known ISBN. GA records
// carry series position and part-within-book as different axes, which is why
// the mapping keeps them separate rather than flattening them into one field.
package graphicaudio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Ebonhawk3829/rhapsode/internal/metadata"
)

// ErrNotFound is returned when the ISBN is not in the GA catalogue.
var ErrNotFound = errors.New("graphicaudio: not found")

// Client is a GraphicAudio scraper client.
type Client struct {
	base string
	http *http.Client
}

// New builds a client. An empty base takes the deployment default.
func New(base string) *Client {
	if base == "" {
		base = "http://100.64.0.1:8092"
	}
	return &Client{
		base: strings.TrimRight(base, "/"),
		http: &http.Client{Timeout: 30 * time.Second},
	}
}

// Name identifies the provider.
func (c *Client) Name() string { return "graphicaudio" }

// Search returns candidate works matching a free-text query.
//
// The scraper has one endpoint for both purposes: /search/{term}. Search and
// Lookup share it, and Lookup narrows the result list to the matching ISBN.
func (c *Client) Search(ctx context.Context, query string) ([]*metadata.Book, error) {
	records, err := c.query(ctx, query)
	if err != nil {
		return nil, err
	}

	out := make([]*metadata.Book, 0, len(records))
	for i := range records {
		out = append(out, records[i].toBook(c.Name()))
	}
	return out, nil
}

// Lookup resolves an ISBN to a single work.
//
// ASINs are rejected rather than attempted: the GA catalogue is keyed by ISBN,
// and an Audible identifier would only ever miss. The caller retries it against
// a provider that does take ASINs.
func (c *Client) Lookup(ctx context.Context, id metadata.Identifier) (*metadata.Book, error) {
	if id.Kind != metadata.ISBN {
		return nil, fmt.Errorf("graphicaudio: identifier kind %q is not supported", id.Kind)
	}
	if id.Value == "" {
		return nil, errors.New("graphicaudio: empty isbn")
	}

	records, err := c.query(ctx, id.Value)
	if err != nil {
		return nil, err
	}
	for i := range records {
		if records[i].ISBN == id.Value {
			b := records[i].toBook(c.Name())
			return b, nil
		}
	}
	return nil, ErrNotFound
}

// query performs the search call and decodes every record.
func (c *Client) query(ctx context.Context, term string) ([]record, error) {
	endpoint := fmt.Sprintf("%s/search/%s", c.base, url.PathEscape(term))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("graphicaudio: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("graphicaudio: search %q: %w", term, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, nil
	default:
		return nil, fmt.Errorf("graphicaudio: search %q: unexpected status %s", term, resp.Status)
	}

	var records []record
	if err := json.NewDecoder(resp.Body).Decode(&records); err != nil {
		return nil, fmt.Errorf("graphicaudio: decode %q: %w", term, err)
	}
	return records, nil
}

// record is the wire shape of one GA scraper result.
//
// The field names are the scraper's own vocabulary — episodeNumber,
// episodePart, episodeCode — where episodeNumber is the *series position* and
// episodePart is the *part within the book*. Those axes are different: a
// series' third book, part two is episodeNumber 3 and episodePart 2 of
// totalParts. Conflating them is what broke the one-off migration script.
type record struct {
	Link       string `json:"link"`
	Cover      string `json:"cover"`
	SeriesName string `json:"seriesName"`
	Title      string `json:"title"`
	RawTitle   string `json:"rawtitle"`
	// EpisodeNumber is the series position and comes back as a number;
	// EpisodePart is the part within the book and comes back as a string.
	// EpisodeCode is the two together, "series.part". They are three views
	// of the same two axes, and the axes are what matter.
	EpisodeNumber json.Number `json:"episodeNumber"`
	EpisodePart   string      `json:"episodePart"`
	EpisodeCode   string      `json:"episodeCode"`
	TotalParts    string      `json:"totalParts"`
	ASIN          string      `json:"asin"`
	Subtitle      string      `json:"subtitle"`
	Author        string      `json:"author"`
	ReleaseDate   string      `json:"releaseDate"`
	ISBN          string      `json:"isbn"`
	Genre         string      `json:"genre"`
	Description   string      `json:"description"`
	Copyright     string      `json:"copyright"`
	Cast          []string    `json:"cast"`
	TitleOther    string      `json:"titleOther"`
	Confidence    float64     `json:"_confidence"`
}

// toBook maps the wire shape onto the domain type.
//
// The narrator is the literal "GraphicAudio" by the library contract: GA
// releases are full-cast productions with dozens of credited voices, and the
// production name identifies both what it is and which edition it is.
func (r *record) toBook(source string) *metadata.Book {
	return &metadata.Book{
		Title:          strings.TrimSpace(r.Title),
		Author:         strings.TrimSpace(r.Author),
		Series:         strings.TrimSpace(r.SeriesName),
		SeriesSequence: string(r.EpisodeNumber),
		Narrator:       "GraphicAudio",
		Publisher:      "GraphicAudio",
		PublishedYear:  publishedYear(r.ReleaseDate),
		Description:    strings.TrimSpace(r.Description),
		Genres:         genres(r.Genre),
		Identifiers:    identifiers(r),
		Source:         source,
	}
}

// identifiers lists the catalogue keys a GA record carries. Most records have
// an ISBN alone; some also carry an ASIN, which is worth keeping because it
// lets an Audnexus lookup enrich the same work without a second search.
func identifiers(r *record) []metadata.Identifier {
	var out []metadata.Identifier
	if v := strings.TrimSpace(r.ISBN); v != "" {
		out = append(out, metadata.Identifier{Kind: metadata.ISBN, Value: v})
	}
	if v := strings.TrimSpace(r.ASIN); v != "" {
		out = append(out, metadata.Identifier{Kind: metadata.ASIN, Value: v})
	}
	return out
}

// publishedYear extracts a bare YYYY from the scraper's release date, which
// is an ISO timestamp like "2011-01-01T00:00:00.000Z".
func publishedYear(releaseDate string) string {
	if len(releaseDate) >= 4 {
		return releaseDate[:4]
	}
	return ""
}

// genres splits the scraper's flat genre field. A record may carry one genre
// or a separated list; both are flattened to names.
func genres(g string) []string {
	if strings.TrimSpace(g) == "" {
		return nil
	}
	parts := strings.FieldsFunc(g, func(r rune) bool {
		return r == ',' || r == ';' || r == '|'
	})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
