// Package audnexus implements metadata.Provider against the public Audnexus
// API.
//
// Audnexus is keyed by ASIN only: it has no search endpoint, so Search is not
// supported and returns ErrNoSearch. Lookup is the whole of its usefulness —
// given an ASIN it returns the edition's narrator, publisher, series and
// identifiers, which is what the library contract needs and what the
// GraphicAudio scraper cannot supply for standard-narration releases.
package audnexus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Ebonhawk3829/rhapsode/internal/metadata"
)

// ErrNoSearch is returned by Search. Audnexus has no search endpoint; the
// omnisearch reaches it through a known ASIN, not a query.
var ErrNoSearch = errors.New("audnexus has no search endpoint")

// ErrNotFound is returned when the ASIN is not in the catalogue.
var ErrNotFound = errors.New("audnexus: not found")

// Client is an Audnexus API client.
type Client struct {
	base   string
	region string
	http   *http.Client
}

// New builds a client. An empty base or region takes the default.
func New(base, region string) *Client {
	if base == "" {
		base = "https://api.audnex.us"
	}
	if region == "" {
		region = "us"
	}
	return &Client{
		base:   strings.TrimRight(base, "/"),
		region: region,
		http:   &http.Client{Timeout: 30 * time.Second},
	}
}

// Name identifies the provider.
func (c *Client) Name() string { return "audnexus" }

// Search is unsupported: Audnexus is ASIN-only.
func (c *Client) Search(context.Context, string) ([]*metadata.Book, error) {
	return nil, ErrNoSearch
}

// Lookup resolves an ASIN to a Book.
//
// An ISBN is rejected rather than attempted: Audnexus keys on ASIN, and a
// numeric identifier that is really an ISBN would 404 anyway. The caller
// decides whether to retry it against a provider that does take ISBNs.
func (c *Client) Lookup(ctx context.Context, id metadata.Identifier) (*metadata.Book, error) {
	if id.Kind != metadata.ASIN {
		return nil, fmt.Errorf("audnexus: identifier kind %q is not supported", id.Kind)
	}
	if id.Value == "" {
		return nil, errors.New("audnexus: empty asin")
	}

	endpoint := fmt.Sprintf("%s/books/%s?region=%s", c.base, url.PathEscape(id.Value), url.QueryEscape(c.region))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("audnexus: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("audnexus: lookup %s: %w", id.Value, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, ErrNotFound
	default:
		return nil, fmt.Errorf("audnexus: lookup %s: unexpected status %s", id.Value, resp.Status)
	}

	var raw book
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("audnexus: decode %s: %w", id.Value, err)
	}
	return raw.toBook(c.Name()), nil
}

// book is the wire shape of an Audnexus book response.
//
// Only the fields rhapsode uses are declared. The API returns more, and
// unknown fields are ignored rather than rejected, so an upstream addition
// does not break the parser.
type book struct {
	ASIN          string     `json:"asin"`
	ISBN          string     `json:"isbn"`
	Title         string     `json:"title"`
	Subtitle      string     `json:"subtitle"`
	Authors       []author   `json:"authors"`
	Narrators     []narrator `json:"narrators"`
	PublisherName string     `json:"publisherName"`
	ReleaseDate   string     `json:"releaseDate"`
	Copyright     int        `json:"copyright"`
	Description   string     `json:"description"`
	Summary       string     `json:"summary"`
	Genres        []genre    `json:"genres"`
	SeriesPrimary *series    `json:"seriesPrimary"`
}

type author struct {
	Name string `json:"name"`
}

type narrator struct {
	Name string `json:"name"`
}

// genre is an object, not a string. Flattening to names happens in toBook.
type genre struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type series struct {
	Name string `json:"name"`
	// Position may pack the series name into the position field, e.g.
	// "1, Kylar Chronicles". parsePosition splits it.
	Position string `json:"position"`
}

// toBook maps the wire shape onto the domain type.
func (b *book) toBook(source string) *metadata.Book {
	out := &metadata.Book{
		Title:       strings.TrimSpace(b.Title),
		Publisher:   strings.TrimSpace(b.PublisherName),
		Description: cleanDescription(b.Description, b.Summary),
		Source:      source,
	}

	if len(b.Authors) > 0 {
		out.Author = strings.TrimSpace(b.Authors[0].Name)
	}
	if len(b.Narrators) > 0 {
		out.Narrator = strings.TrimSpace(b.Narrators[0].Name)
	}

	out.PublishedYear = publishedYear(b.ReleaseDate, b.Copyright)

	for _, g := range b.Genres {
		if name := strings.TrimSpace(g.Name); name != "" {
			out.Genres = append(out.Genres, name)
		}
	}

	if b.SeriesPrimary != nil {
		out.Series = strings.TrimSpace(b.SeriesPrimary.Name)
		out.SeriesSequence = parsePosition(b.SeriesPrimary.Position)
	}

	if b.ASIN != "" {
		out.Identifiers = append(out.Identifiers, metadata.Identifier{Kind: metadata.ASIN, Value: b.ASIN})
	}
	if b.ISBN != "" {
		out.Identifiers = append(out.Identifiers, metadata.Identifier{Kind: metadata.ISBN, Value: b.ISBN})
	}
	return out
}

// parsePosition extracts the leading number from a series position.
//
// Audnexus sometimes packs the series name into the position field —
// "1, Kylar Chronicles" — so the value cannot be used verbatim. The leading
// integer is the position; anything after it is a series-name hint that the
// caller already has from seriesPrimary.name, so it is discarded.
func parsePosition(pos string) string {
	pos = strings.TrimSpace(pos)
	if pos == "" {
		return ""
	}
	if i := strings.IndexByte(pos, ','); i >= 0 {
		pos = strings.TrimSpace(pos[:i])
	}
	return pos
}

// publishedYear prefers the release date and falls back to the copyright year.
// The result is a bare YYYY, which is what the library contract stores.
func publishedYear(releaseDate string, copyright int) string {
	if releaseDate != "" {
		if t, err := time.Parse(time.RFC3339, releaseDate); err == nil {
			return strconv.Itoa(t.Year())
		}
		// Some records carry a date without a time component.
		if t, err := time.Parse("2006-01-02", releaseDate); err == nil {
			return strconv.Itoa(t.Year())
		}
	}
	if copyright > 0 {
		return strconv.Itoa(copyright)
	}
	return ""
}

// cleanDescription prefers the plain description and falls back to the HTML
// summary with its tags stripped. The library contract stores text, not markup.
func cleanDescription(description, summary string) string {
	if d := strings.TrimSpace(description); d != "" {
		return d
	}
	return stripHTML(summary)
}

// stripHTML removes tags and collapses whitespace. It is deliberately simple:
// the input is publisher blurb markup, not arbitrary HTML, so a full parser
// would be more machinery than the job needs.
func stripHTML(s string) string {
	var b strings.Builder
	b.Grow(len(s))

	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}

	return strings.Join(strings.Fields(b.String()), " ")
}
