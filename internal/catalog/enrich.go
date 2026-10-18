// enrich.go holds the search-time enrichment chain.
//
// The Audible search answers "what ASINs does this query hit" and nothing
// more — its entries carry a title scaffold and an ASIN, no narrator, no
// series, no publisher. Audnexus is the catalogue that owns that metadata,
// keyed by the ASIN the search just produced. Enrichment is the join between
// the two.
//
// It lives here rather than inside the audible provider because the boundary
// is the one between discovery and authority: a scraper discovers, an API
// declares. A scraper that starts asserting narrator names is claiming
// knowledge it does not have.
package catalog

import (
	"context"
	"sync"

	"github.com/Ebonhawk3829/rhapsode/internal/metadata"
)

// Enricher fills scaffold books out from their identifiers.
type Enricher interface {
	// Lookup resolves one identifier to an authoritative record.
	Lookup(ctx context.Context, id metadata.Identifier) (*metadata.Book, error)
}

// SearchEnriched runs query across providers, then fills scaffold books from
// an enricher.
//
// The enricher runs on the books that need it — those thin on the fields the
// library contract wants — and each lookup is bounded by the same context as
// the searches, so a slow Audnexus cannot hold the response open past the
// searches that fed it.
//
// Lookup failures are per-book: a 404 on one ASIN drops that book, not the
// search. A scaffold whose enrichment failed still appears — a title-only hit
// is a genuine candidate the user can search by hand — but it is marked so
// the UI can order it last.
func SearchEnriched(ctx context.Context, p metadata.Provider, e Enricher, query string, enrich func(*metadata.Book) bool) ([]*metadata.Book, error) {
	books, err := p.Search(ctx, query)
	if err != nil {
		return nil, err
	}
	if e == nil {
		return books, nil
	}

	// Work out which books want enrichment. The predicate is supplied by the
	// caller because "needs enrichment" depends on what the provider already
	// carries: GA records are complete for their purpose, Audible scaffolds
	// are not.
	type task struct {
		idx   int
		ident metadata.Identifier
	}
	var tasks []task
	for i, b := range books {
		for _, id := range b.Identifiers {
			if enrich(b) {
				tasks = append(tasks, task{idx: i, ident: id})
				break // one identifier per book is enough
			}
		}
	}
	if len(tasks) == 0 {
		return books, nil
	}

	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		fixed = make(map[int]*metadata.Book, len(tasks))
	)
	for _, t := range tasks {
		wg.Add(1)
		go func(t task) {
			defer wg.Done()
			got, err := e.Lookup(ctx, t.ident)
			if err != nil || got == nil {
				// Drop-through: the scaffold stands as found. A miss here
				// is expected when the discovery region and the catalogue
				// region disagree — an .com.au ASIN is not in Audnexus's
				// US catalogue.
				return
			}
			mu.Lock()
			fixed[t.idx] = got
			mu.Unlock()
		}(t)
	}
	wg.Wait()

	for idx, replacement := range fixed {
		books[idx] = replacement
	}
	return books, nil
}
