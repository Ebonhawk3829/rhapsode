// Package abb implements the AudioBookBay source: search the catalogue, and
// resolve a listing into a magnet from the info hash the page publishes.
//
// Every request goes through FlareSolverr. ABB sits behind browser-selective
// throttling that 503s datacentre IPs; the Audible provider proved the
// FlareSolverr path, and this one reuses it. There is no direct-fetch
// fallback on purpose: a fallback that half-works teaches the caller to
// depend on flaky behaviour instead of treating ABB as unavailable.
//
// The magnet needs no site-side endpoint. ABB's own JS asks
// /download-magnet.php?h=<hash> to bless the hash, and that endpoint answers
// "invalid" to anonymous requests (its success path redirects to login) —
// probed live, 2026-10-18. But the page always shows the raw Info Hash, and
// main.js's own "valid" branch builds the magnet from exactly that hash plus
// a hardcoded tracker list. Rhapsode takes that branch always: read the hash
// from the page, magnet it with the trackers below. The trackers are extracted
// from main.js (testdata holds the extraction too, in trackers.txt) and are
// the site's canonical list, so health behaves like any ABB client's magnet.
package abb

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// defaultTrackers is main.js's "valid" tracker list, URL-encoded as the site
// sends it (decoded once here; the client re-encodes when building magnets).
// Splitting the list keeps Golang magnet building readable.
var defaultTrackers = []string{
	"udp://tracker.torrent.eu.org:451/announce",
	"udp://tracker.opentrackr.org:1337/announce",
	"udp://open.stealth.si:80/announce",
	"udp://tracker.dler.org:6969/announce",
	"udp://bittorrent-tracker.e-n-c-r-y-p-t.net:1337/announce",
	"udp://retracker01-msk-virt.corbina.net:80/announce",
	"http://ipv4announce.sktorrent.eu:6969/announce",
	"http://tracker.bt4g.com:2095/announce",
	"http://tracker.mywaifu.best:6969/announce",
}

// ErrUnavailable marks ABB being unreachable or bot-walled for this request.
// The runner treats it the way it treats a dead source: skip, log, carry on.
var ErrUnavailable = errors.New("abb: unavailable")

// flareTalker is the part of FlareSolverr the client needs. A small interface
// keeps the flare solver mockable and the client testable against fixtures.
type flareTalker interface {
	Get(ctx context.Context, url string) (string, error) // returns page HTML
}

// Client queries AudioBookBay through FlareSolverr.
type Client struct {
	base string // e.g. https://audiobookbay.lu
	// Mirrors are alternate base hosts tried in order when the primary
	// answers unavailable. ABB rotates domains; order matters to the user,
	// so the list is config, not code.
	Mirrors []string
	// Pages caps how deep a search walks. Each page is one browser request
	// through FlareSolverr, so the cap is a ban-risk dial, not a correctness
	// knob: deeper history is rarely worth the exposure.
	Pages int
	flare flareTalker
}

// New builds a client over a FlareSolverr talker. base is the scheme and
// host (default audiobookbay.lu).
func New(base string, flare flareTalker) *Client {
	if !strings.HasPrefix(base, "http") {
		base = "https://" + base
	}
	return &Client{
		base:  strings.TrimSuffix(base, "/"),
		Pages: 3,
		flare: flare,
	}
}

// fetching the detail page.
type listing struct {
	// DetailPath is the site-absolute path of the torrent's own page —
	// /abss/<slug>/ — the magnet's source of truth.
	DetailPath  string
	Title       string
	Language    string
	Format      string
	BitrateKbps int
	SizeMB      int64
	// Posted is "12 Sep 2026" verbatim; displayed, not parsed.
	Posted string
}

// Search walks up to c.Pages search pages and returns the listings found.
//
// The query goes into ABB's WordPress search (`?s=`); it is joined with '+'
// because ABB's engine treats '+' as a separator but '+' inside words as part
// of the word, which the Audible provider's probing already documented.
func (c *Client) Search(ctx context.Context, query string) ([]listing, error) {
	var out []listing
	for page := 1; page <= c.pageCap(); page++ {
		url := fmt.Sprintf("%s/page/%d/?s=%s", c.base, page, strings.ReplaceAll(strings.ToLower(query), " ", "+"))
		html, err := c.flare.Get(ctx, url)
		if err != nil {
			return out, fmt.Errorf("%w: page %d: %v", ErrUnavailable, page, err)
		}
		if isNotFound(html) {
			break
		}
		found := parseSearchPage(c.base, html)
		if len(found) == 0 {
			break // no posts on this page: walked past the results
		}
		out = append(out, found...)
	}
	return out, nil
}

// Magnet resolves a listing into a magnet URI.
//
// It fetches the listing's detail page and reads the Info Hash row. If the
// page has no hash (deleted listing, or ABB markup drift), that is a
// per-candidate failure — returned as an error the confirm screen can show
// without failing the whole job.
func (c *Client) Magnet(ctx context.Context, l listing) (string, error) {
	if l.DetailPath == "" {
		return "", errors.New("abb: listing has no detail path")
	}
	html, err := c.flare.Get(ctx, c.base+l.DetailPath)
	if err != nil {
		return "", fmt.Errorf("%w: detail: %v", ErrUnavailable, err)
	}
	hash := extractInfoHash(html)
	if hash == "" {
		return "", fmt.Errorf("abb: no info hash on %s", l.DetailPath)
	}
	return magnetFor(hash, l.Title), nil
}

// magnetFor builds a magnet from a hash and its trackers.
//
// btih hashes are case-insensitive at the tracker level but lowercase is what
// ABB shows and what transmission echoes back, so the value round-trips
// deterministically.
func magnetFor(hash, title string) string {
	enc := func(s string) string { return urlQueryEscape(s) }
	q := "magnet:?xt=urn:btih:" + strings.ToLower(hash)
	if title != "" {
		q += "&dn=" + enc(title)
	}
	for _, t := range defaultTrackers {
		q += "&tr=" + enc(t)
	}
	return q
}

// urlQueryEscape percent-encodes for a query string the way the site's JS
// does. net/url.QueryEscape is close but escapes differently in ways that
// leak into magnet readability; a small table keeps the magnet byte-stable
// with ABB's own links.
func urlQueryEscape(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		case c == ' ':
			b.WriteByte('+')
		default:
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0xf])
		}
	}
	return b.String()
}

var (
	// isNotFound does a cheap check rather than a full parse: ABB's search
	// returns the site shell ("Nothing Found") with HTTP 200.
	notFoundRe        = regexp.MustCompile(`(?i)nothing found|not found for`)
	noResultsSentinel = `class="postTitle`
)

func isNotFound(html string) bool {
	return strings.Contains(html, notFoundRe.String()) &&
		!strings.Contains(html, noResultsSentinel)
}

// parseSearchPage walks `.post` blocks. See fixtures for the shapes.

var (
	postOpenRe   = regexp.MustCompile(`<div class="post[ "][^>]*>|<div class="post">`)
	linkRe       = regexp.MustCompile(`<div class="postTitle"><h2><a href="([^"]+)"[^>]*>([^<]+)</a>`)
	langRe       = regexp.MustCompile(`Language:\s*([a-zA-Z ]+?)\s*(?:<span|Keywords)`)
	formatRe     = regexp.MustCompile(`Format:\s*<span[^>]*>([^<]+)`)
	bitrateRe    = regexp.MustCompile(`Bitrate:\s*<span[^>]*>([^<]+)`)
	sizeRe       = regexp.MustCompile(`File Size:\s*<span[^>]*>([\d.]+)</span>\s*([^<]+)`)
	postedRe     = regexp.MustCompile(`Posted:\s*([^<]+)`)
	detailLinkRe = regexp.MustCompile(`<span class="postLink"><a href="([^"]+)"`)
)

// parseSearchPage extracts listings from one search-results page.
//
// The regexes are anchored to the exact class names ABB ships; when they
// change, the fixtures fail before search returns garbage. Everything on a
// row varies in format ("128 Kbps", "7.42 GBs"), so numeric parsing is best
// effort and a row with unparseable units still surfaces — an imprecise
// candidate beats a silently dropped one, because the confirm screen is the
// judge.
func parseSearchPage(base, html string) []listing {
	var out []listing
	// Block boundaries are the opens themselves: ABB decorates posts with
	// extra classes ("post re-ab") and the closers vary in nesting depth, so
	// matching a closer reliably loses rows. Slicing between consecutive
	// opens keeps every row.
	opens := postOpenRe.FindAllStringIndex(html, -1)
	for i, open := range opens {
		end := len(html)
		if i+1 < len(opens) {
			end = opens[i+1][0]
		}
		body := html[open[1]:end]
		l := listing{}
		if m := linkRe.FindStringSubmatch(body); m != nil {
			l.DetailPath = toPath(base, m[1])
			l.Title = strings.TrimSpace(m[2])
		}
		// The postLink span carries the absolute detail URL; prefer it for
		// exactness, fall back to the title link.
		if m := detailLinkRe.FindStringSubmatch(body); m != nil {
			l.DetailPath = toPath(base, m[1])
		}
		if m := langRe.FindStringSubmatch(body); m != nil {
			l.Language = strings.TrimSpace(m[1])
		}
		if m := formatRe.FindStringSubmatch(body); m != nil {
			l.Format = strings.TrimSpace(strings.ToLower(m[1]))
		}
		if m := bitrateRe.FindStringSubmatch(body); m != nil {
			l.BitrateKbps = parseBitrate(m[1])
		}
		if m := sizeRe.FindStringSubmatch(body); m != nil {
			l.SizeMB = parseSizeMB(m[1], m[2])
		}
		if m := postedRe.FindStringSubmatch(body); m != nil {
			l.Posted = strings.TrimSpace(m[1])
		}
		if l.DetailPath == "" || l.Title == "" {
			continue // not a listing row; ads and page furniture sneak in
		}
		out = append(out, l)
	}
	return out
}

// toPath reduces a possibly-absolute detail URL to the site path, so Magnet
// always prefixes one base.
func toPath(base, url string) string {
	if strings.HasPrefix(url, base) {
		return strings.TrimPrefix(url, base)
	}
	return url
}

// parseBitrate reads "128 Kbps" / "64 kbps" / "VBR" into kbps. Zero if not
// numeric: 0 means unknown to the scorer, never "why is this zero?".
func parseBitrate(s string) int {
	s = strings.TrimSpace(strings.ToLower(s))
	if strings.Contains(s, "vbr") || strings.Contains(s, "variable") {
		return 0
	}
	n, _, _ := splitNumber(s)
	return n
}

// parseSizeMB reads "7.42" + "GBs" (or "412.50" + "MBs") into megabytes.
func parseSizeMB(num, unit string) int64 {
	f, ok := parseFloat(num)
	if !ok {
		return 0
	}
	unit = strings.ToLower(strings.TrimSpace(unit))
	switch {
	case strings.HasPrefix(unit, "g"):
		return int64(f * 1024)
	case strings.HasPrefix(unit, "m"):
		return int64(f)
	case strings.HasPrefix(unit, "t"):
		return int64(f * 1024 * 1024)
	default:
		return 0
	}
}

// splitNumber reads a leading integer out of a string like "128 kbps".
func splitNumber(s string) (int, string, string) {
	digits := ""
	rest := ""
	for i, r := range s {
		if r >= '0' && r <= '9' {
			digits += string(r)
		} else {
			rest = strings.TrimSpace(s[i:])
			break
		}
	}
	n, _ := strconv.Atoi(digits)
	return n, digits, rest
}

func parseFloat(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	// keep only digits, dot
	var b strings.Builder
	for _, r := range s {
		if (r >= '0' && r <= '9') || r == '.' {
			b.WriteRune(r)
		} else if b.Len() > 0 {
			break
		}
	}
	f, err := strconv.ParseFloat(b.String(), 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

// extractInfoHash reads the 40-hex Info Hash row from a detail page.
var infoHashRe = regexp.MustCompile(`Info Hash:\s*</td>\s*<td>\s*([a-f0-9]{40})`)

func extractInfoHash(html string) string {
	m := infoHashRe.FindStringSubmatch(html)
	if m == nil {
		return ""
	}
	return m[1]
}

// pageCap guards a zero-value Client: zero Pages would search nothing.
func (c *Client) pageCap() int {
	if c.Pages <= 0 {
		return 3
	}
	return c.Pages
}
