// Package audible implements metadata.Provider by scraping Audible's search
// results page.
//
// Audible has no public search API: api.audible.com requires signed requests,
// and api.audnex.us is ASIN-keyed lookup only. The search page is the
// remaining title→ASIN bridge, and it is a scraping surface — server-rendered
// (no JS needed), but throttled with intermittent 503s and one redesign away
// from breaking.
//
// The provider is therefore written defensively at every level: 503 is treated
// as "unavailable right now" rather than an error, parse failures are scoped
// per item so one malformed result cannot cost the page, and a page that
// yields nothing parseable returns empty rather than raising — the merge layer
// in catalog already degrades gracefully per provider.
package audible

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Ebonhawk3829/rhapsode/internal/metadata"
)

// ErrUnavailable marks a search that could not be completed for a reason the
// caller should ride out: throttled, challenge page, or an empty result set.
// It is distinct from a parse error, which is worth complaining about.
var ErrUnavailable = errors.New("audible: unavailable")

// flareRequest is the body of a FlareSolverr /v1 request.
type flareRequest struct {
	Cmd        string `json:"cmd"`
	URL        string `json:"url"`
	MaxTimeout int    `json:"maxTimeout"`
	Session    string `json:"session,omitempty"`
}

// flareResponse is the shape of a FlareSolverr reply. Only the resolved page
// is interesting; status tells us whether the challenge was actually beaten.
type flareResponse struct {
	Status   string `json:"status"`
	Message  string `json:"message"`
	Solution struct {
		URL      string `json:"url"`
		Status   int    `json:"status"`
		Response string `json:"response"`
	} `json:"solution"`
}

// FlareSolverr is a challenge-solving proxy. Audible geo-redirects datacentre
// IPs hard (this server lands on .com.au) and 503s direct hits on the .com.au
// storefront; routed through the proxy both walls fall, at the cost of a
// slower page — a few seconds against a few hundred milliseconds.
type FlareSolverr struct {
	base string
	http *http.Client
}

// NewFlareSolverr builds a proxy client. An empty base takes the deployment
// default.
func NewFlareSolverr(base string) *FlareSolverr {
	if base == "" {
		base = "http://100.64.0.1:8191"
	}
	return &FlareSolverr{
		base: strings.TrimRight(base, "/"),
		http: &http.Client{Timeout: 90 * time.Second},
	}
}

// Get resolves a URL through the proxy and returns the page HTML.
func (f *FlareSolverr) Get(ctx context.Context, target string) ([]byte, error) {
	body, err := json.Marshal(flareRequest{
		Cmd:        "request.get",
		URL:        target,
		MaxTimeout: 60000,
	})
	if err != nil {
		return nil, fmt.Errorf("flaresolverr: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.base+"/v1", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("flaresolverr: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := f.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("flaresolverr: %w", err)
	}
	defer resp.Body.Close()

	var fr flareResponse
	if err := json.NewDecoder(resp.Body).Decode(&fr); err != nil {
		return nil, fmt.Errorf("flaresolverr: decode response: %w", err)
	}
	if fr.Status != "ok" {
		return nil, fmt.Errorf("flaresolverr: %s: %s", fr.Status, fr.Message)
	}
	if fr.Solution.Status < 200 || fr.Solution.Status >= 300 {
		// The proxy/report wrangling succeeded but the inner page did not —
		// an upstream 503 rides out the same way a direct one would.
		return nil, ErrUnavailable
	}
	return []byte(fr.Solution.Response), nil
}

// Client scrapes Audible search pages.
type Client struct {
	// base is the storefront to search. Different regions order results
	// differently and hold different catalogues; the enricher's region should
	// agree with it. Geo-redirects make the "right" region the one the
	// server's IP lands in, not the one the config asks for.
	base string
	// flare resolves the page through a challenge-solving proxy. Audible
	// geo-redirects and throttles datacentre IPs; direct fetches worked from
	// a desktop network and fail from this server.
	flare *FlareSolverr
}

// New builds a client. An empty flare endpoint disables the proxy, which is
// only useful from a residential IP.
func New(base, flareEndpoint string) *Client {
	if base == "" {
		base = "https://www.audible.com"
	}
	var flare *FlareSolverr
	if flareEndpoint != "" {
		flare = NewFlareSolverr(flareEndpoint)
	}
	return &Client{
		base:  strings.TrimRight(base, "/"),
		flare: flare,
	}
}

// Name identifies the provider.
func (c *Client) Name() string { return "audible" }

// Lookup is unsupported: Audible search gives ASINs; enrichment is the
// Audnexus provider's job. The two are deliberately separate so the scrape
// stays dumb and the catalogue stays official.
func (c *Client) Lookup(context.Context, metadata.Identifier) (*metadata.Book, error) {
	return nil, errors.New("audible: lookup is not supported; use the audnexus provider")
}

// Search resolves a title/author/series query to candidate books, ASIN and
// title only. Metadata-enrichment happens downstream.
//
// The query goes through with %20-encoded spaces. The plus-joined form was
// observed returning unrelated results; the encoded form returned the right
// ones, so the encoding is load-bearing, not cosmetic.
func (c *Client) Search(ctx context.Context, query string) ([]*metadata.Book, error) {
	// url.QueryEscape emits "+" for spaces, which live probes showed
	// returning unrelated results; the raw path escape emits "%20", which is
	// what the successful probes used. Escaping the target path with the
	// strict rule is deliberate.
	target := c.base + "/search?keywords=" + strings.ReplaceAll(url.PathEscape(strings.TrimSpace(query)), "%2F", "/")

	var (
		page []byte
		err  error
	)
	if c.flare != nil {
		page, err = c.flare.Get(ctx, target)
	} else {
		page, err = fetchDirect(ctx, target)
	}
	if err != nil {
		if errors.Is(err, ErrUnavailable) {
			return nil, ErrUnavailable
		}
		return nil, fmt.Errorf("audible: search %q: %w", query, err)
	}
	return parsePage(page), nil
}

// fetchDirect is the raw path, kept for hosts whose IP Audible does not
// geo-redirect. It is not the path a datacentre takes; it is the path a
// home-server deployment might.
func fetchDirect(ctx context.Context, target string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Accept", "text/html")

	var net http.Client
	resp, err := net.Do(req)
	if err != nil {
		return nil, fmt.Errorf("audible: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusServiceUnavailable, http.StatusForbidden, http.StatusTooManyRequests:
		return nil, ErrUnavailable
	default:
		return nil, fmt.Errorf("audible: unexpected status %s", resp.Status)
	}

	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}

// productItem matches one result block in the server-rendered page.
//
// The id attribute carries the ASIN. Only the first 9000 bytes after the id
// are searched for the other fields — a bound that keeps one pathological
// block from scanning the whole document.
var (
	productItem = regexp.MustCompile(`productListItem" id="product-list-item-([A-Z0-9]{10})"`)
	pdLink      = regexp.MustCompile(`href="/pd/([^"/]+)/`)
	titleSlot   = regexp.MustCompile(`slot="title">([^<]{3,90})`)
)

// parsePage extracts every result it can, skipping unparsable blocks.
func parsePage(page []byte) []*metadata.Book {
	html := string(page)
	hits := productItem.FindAllStringSubmatchIndex(html, -1)

	var out []*metadata.Book
	for i, hit := range hits {
		asin := html[hit[2]:hit[3]]
		// The block runs to the next product item, or to the end for the last.
		end := len(html)
		if i+1 < len(hits) {
			end = hits[i+1][0]
		}
		block := html[hit[0]:end]

		b := parseBlock(asin, []byte(block))
		if b != nil {
			out = append(out, b)
		}
	}
	return out
}

// parseBlock maps one result block onto a Book.
//
// The title comes from the /pd/ link slug, dashes for spaces, because the
// title slot is not reliably present for every block. The slug is the
// cleanest title text on the page and is the same string a human would read
// off the result.
func parseBlock(asin string, block []byte) *metadata.Book {
	b := &metadata.Book{
		Identifiers: []metadata.Identifier{{Kind: metadata.ASIN, Value: asin}},
		Source:      "audible",
	}

	if m := pdLink.FindSubmatch(block); m != nil {
		b.Title = slugTitle(string(m[1]))
	} else if m := titleSlot.FindSubmatch(block); m != nil {
		b.Title = strings.TrimSpace(string(m[1]))
	} else {
		// No title anywhere means this block is decoration, not a product.
		return nil
	}
	if b.Title == "" {
		return nil
	}
	return b
}

// slugTitle turns a /pd/ slug into a title: "Night-Angel-Nemesis-Audiobook".
// The trailing "-Audiobook" is decoration the URL scheme appends.
func slugTitle(slug string) string {
	slug = strings.TrimSuffix(slug, "-Audiobook")
	slug = strings.ReplaceAll(slug, "-", " ")
	return strings.TrimSpace(slug)
}
