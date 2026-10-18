// Package catalog is rhapsode's omnisearch: the layer that asks every
// provider and turns their releases into the works the UI shows.
//
// The providers answer "what exists for this query". This layer answers "what
// should the user see". Those are different questions because the providers
// speak in releases — three ISBNs for the three parts of one GA book — while
// the user thinks in works.
package catalog

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/Ebonhawk3829/rhapsode/internal/metadata"
)

// Catalog is the omnisearch.
type Catalog struct {
	providers []metadata.Provider
	// enricher fills scaffold books out from their identifiers. Optional:
	// nil means providers are already authoritative on their own.
	enricher Enricher
}

// New builds a Catalog over the given providers, in query order.
//
// Order is meaningful: GA is first because its releases are what rhapsode
// specialises in, and results are named after the provider that produced
// them, so a stable order keeps the UI stable too.
func New(providers ...metadata.Provider) *Catalog {
	return &Catalog{providers: providers}
}

// WithEnricher attaches the enrichment chain.
func (c *Catalog) WithEnricher(e Enricher) *Catalog {
	c.enricher = e
	return c
}

// needsEnrichment reports whether a book is a scaffold rather than an
// authoritative record.
//
// A book wants enrichment when it is thin on the fields the library contract
// needs and carries precisely one identifier pointing at the authority. A GA
// record — full narrator, publisher, ISBN — is complete for its purpose; an
// Audible search hit — title and ASIN only — is not. A record that carries
// several identifiers already merged two catalogues' views and needs nothing.
func needsEnrichment(b *metadata.Book) bool {
	if len(b.Identifiers) != 1 {
		return false
	}
	id := b.Identifiers[0]
	if id.Kind != metadata.ASIN {
		return false
	}
	// The scaffold's signature fields are empty. Narrator and publisher are
	// the pain points: they drive the library layout and nothing can guess
	// them.
	return b.Narrator == "" && b.Publisher == ""
}

// Search asks every provider and merges the results into works.
//
// A provider that fails does not fail the search: its results are skipped and
// the others stand. The omnisearch is a fan-out, not a transaction — one dead
// catalogue should not blank the page.
func (c *Catalog) Search(ctx context.Context, query string) ([]*Work, error) {
	type result struct {
		books []*metadata.Book
		err   error
	}

	results := make([]result, len(c.providers))
	var wg sync.WaitGroup
	for i, p := range c.providers {
		wg.Add(1)
		go func(i int, p metadata.Provider) {
			defer wg.Done()
			var (
				books []*metadata.Book
				err   error
			)
			if c.enricher != nil {
				books, err = SearchEnriched(ctx, p, c.enricher, query, needsEnrichment)
			} else {
				books, err = p.Search(ctx, query)
			}
			results[i] = result{books: books, err: err}
		}(i, p)
	}
	wg.Wait()

	var works []*Work
	for _, r := range results {
		if r.err != nil {
			continue
		}
		works = merge(works, r.books)
	}
	return works, nil
}

// Work is one book as the user thinks of it, with every release known for it.
//
// The work is the identity the UI keys on: "The Way of Kings". The releases
// are the editions — a GA dramatisation divided into parts, a standard
// narration as one file — and each carries the identifier its acquisition
// route needs.
type Work struct {
	Title    string
	Author   string
	Series   string
	Releases []*Release
}

// Release is one edition of a Work.
type Release struct {
	// Identifier is this edition's catalogue key.
	Identifier metadata.Identifier
	// Narrator distinguishes editions of the same work: "GraphicAudio" for a
	// dramatisation, the reader's name otherwise.
	Narrator string
	// Publisher is the publishing house.
	Publisher string
	// PublishedYear is a bare YYYY.
	PublishedYear string
	// Source is the provider that produced this release.
	Source string
	// Parts is how many parts the release ships in. 1 for a single file.
	Parts int
}

// merge folds books into works and releases.
//
// Identity is author plus normalised title. The normalisation is deliberately
// blunt: case, punctuation and the bracketed "[Dramatized Adaptation]"
// qualifier the GA scraper leaves in raw titles all fold away. Two providers
// naming the same book differently should land on the same work; two genuinely
// different books with the same author and title are rare enough that merging
// them costs less than showing duplicates.
func merge(works []*Work, books []*metadata.Book) []*Work {
	for _, b := range books {
		if b == nil {
			continue
		}
		key := workKey(b.Author, b.Title)

		w := find(works, key)
		if w == nil {
			w = &Work{
				Title:  b.Title,
				Author: b.Author,
				Series: b.Series,
			}
			works = append(works, w)
		}

		for _, id := range b.Identifiers {
			if hasRelease(w, id) {
				continue
			}
			w.Releases = append(w.Releases, &Release{
				Identifier:    id,
				Narrator:      b.Narrator,
				Publisher:     b.Publisher,
				PublishedYear: b.PublishedYear,
				Source:        b.Source,
				Parts:         1,
			})
		}
	}

	sortWorks(works)
	return works
}

// workKey is the merge identity for a book.
//
// Leading articles are dropped before the key is taken: one catalogue says
// "Black Prism", another "The Black Prism", and they are the same book. The
// article is displayed wherever the first-seen title came from; it is not part
// of identity.
//
// Articles are matched on words, not prefixes: normalisation has removed
// whitespace, so the raw title — not the normalised one — is the thing that
// can tell "The Black Prism" from "Theatre". Tests pin "Theatre", "Alex" and
// "An End" so the distinction cannot silently regress.
func workKey(author, rawTitle string) string {
	return strings.ToLower(normalise(author)) + "|" + strings.ToLower(normalise(dropArticle(rawTitle)))
}

// dropArticle removes a leading "The ", "A " or "An " from a title.
//
// It runs on the raw title so word boundaries are still visible. A prefix
// match is only an article when the next character is a space — "The Black"
// strips, "Theatre" does not.
func dropArticle(title string) string {
	for _, a := range []string{"The ", "A ", "An "} {
		if strings.HasPrefix(title, a) {
			return title[len(a):]
		}
	}
	return title
}

// normalise strips everything that catalogues vary on: punctuation,
// whitespace, and the bracketed qualifiers like "[Dramatized Adaptation]".
func normalise(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "["); i >= 0 {
		if j := strings.Index(s[i:], "]"); j >= 0 {
			s = s[:i] + s[i+j+1:]
		}
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ':
			// collapsed below
		}
	}
	return b.String()
}

// find returns the work with the given key, or nil.
func find(works []*Work, key string) *Work {
	for _, w := range works {
		if workKey(w.Author, w.Title) == key {
			return w
		}
	}
	return nil
}

// hasRelease reports whether the work already carries this identifier.
func hasRelease(w *Work, id metadata.Identifier) bool {
	for _, r := range w.Releases {
		if r.Identifier.Kind == id.Kind && r.Identifier.Value == id.Value {
			return true
		}
	}
	return false
}

// sortWorks orders by author, then series, then title.
func sortWorks(works []*Work) {
	sort.Slice(works, func(i, j int) bool {
		a, b := works[i], works[j]
		if a.Author != b.Author {
			return a.Author < b.Author
		}
		if a.Series != b.Series {
			return a.Series < b.Series
		}
		return a.Title < b.Title
	})
}
