package catalog

import (
	"context"
	"errors"
	"testing"

	"github.com/Ebonhawk3829/rhapsode/internal/metadata"
)

// fake is a provider with scripted answers.
type fake struct {
	name  string
	books []*metadata.Book
	err   error
}

func (f *fake) Name() string { return f.name }

func (f *fake) Search(_ context.Context, _ string) ([]*metadata.Book, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.books, nil
}

func (f *fake) Lookup(_ context.Context, _ metadata.Identifier) (*metadata.Book, error) {
	return nil, errors.New("not implemented")
}

func gaBook(isbn, title string) *metadata.Book {
	return &metadata.Book{
		Title:     title,
		Author:    "Brent Weeks",
		Series:    "Lightbringer Saga",
		Narrator:  "GraphicAudio",
		Publisher: "GraphicAudio",
		// The GA scraper leaves "[Dramatized Adaptation]" in raw titles; the
		// normalisation must fold it so GA and Audnexus records of the same
		// book land on one Work.
		Identifiers: []metadata.Identifier{{Kind: metadata.ISBN, Value: isbn}},
		Source:      "graphicaudio",
	}
}

func audibleBook(asin string) *metadata.Book {
	return &metadata.Book{
		Title:       "The Black Prism",
		Author:      "Brent Weeks",
		Series:      "Lightbringer Saga",
		Narrator:    "Simon Vance",
		Publisher:   "Little, Brown Audio",
		Identifiers: []metadata.Identifier{{Kind: metadata.ASIN, Value: asin}},
		Source:      "audnexus",
	}
}

func TestMergeFoldsReleasesIntoOneWork(t *testing.T) {
	c := New(
		&fake{name: "graphicaudio", books: []*metadata.Book{
			gaBook("9781648794384", "Black Prism [Dramatized Adaptation]"),
			gaBook("9781648794407", "Black Prism [Dramatized Adaptation]"),
		}},
		&fake{name: "audnexus", books: []*metadata.Book{
			audibleBook("B004AXE6FY"),
		}},
	)

	works, err := c.Search(context.Background(), "black prism")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(works) != 1 {
		t.Fatalf("got %d works, want 1 — three releases, one book", len(works))
	}

	w := works[0]
	if len(w.Releases) != 3 {
		t.Fatalf("got %d releases, want 3", len(w.Releases))
	}
	kinds := map[metadata.IdentifierKind]int{}
	for _, r := range w.Releases {
		kinds[r.Identifier.Kind]++
	}
	if kinds["isbn"] != 2 || kinds["asin"] != 1 {
		t.Errorf("releases = %v, want 2 ISBN + 1 ASIN", kinds)
	}
}

func TestFailedProviderDoesNotFailSearch(t *testing.T) {
	c := New(
		&fake{name: "graphicaudio", books: []*metadata.Book{gaBook("9781648794384", "Black Prism")}},
		&fake{name: "audnexus", err: errors.New("network down")},
	)

	works, err := c.Search(context.Background(), "black prism")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(works) != 1 || len(works[0].Releases) != 1 {
		t.Fatalf("got %v, want the GA result standing alone", works)
	}
}

func TestAllProvidersFailingIsEmpty(t *testing.T) {
	c := New(
		&fake{name: "graphicaudio", err: errors.New("down")},
		&fake{name: "audnexus", err: errors.New("down")},
	)

	works, err := c.Search(context.Background(), "black prism")
	if err != nil {
		t.Fatalf("Search should not fail when every provider fails: %v", err)
	}
	// A search where nothing could be asked is an empty page, not an error:
	// the UI renders "no match", which is true in spirit if not in cause.
	if len(works) != 0 {
		t.Errorf("got %d works, want 0", len(works))
	}
}

func TestDifferentBooksStaySeparate(t *testing.T) {
	c := New(
		&fake{name: "graphicaudio", books: []*metadata.Book{
			gaBook("9781648794384", "Black Prism"),
			gaBook("9780316044400", "The Way of Shadows"),
		}},
	)

	works, _ := c.Search(context.Background(), "brent weeks")
	if len(works) != 2 {
		t.Fatalf("got %d works, want 2", len(works))
	}
}

func TestSortOrder(t *testing.T) {
	c := New(&fake{name: "graphicaudio", books: []*metadata.Book{
		gaBook("1", "The Way of Shadows"),
		gaBook("2", "Black Prism"),
	}})

	works, _ := c.Search(context.Background(), "brent weeks")
	if len(works) != 2 {
		t.Fatalf("got %d works", len(works))
	}
	if works[0].Title != "Black Prism" || works[1].Title != "The Way of Shadows" {
		t.Errorf("order = %q, %q — want Black Prism first (title sort within one author)", works[0].Title, works[1].Title)
	}
}

func TestDropArticleEdges(t *testing.T) {
	cases := map[string]string{
		"The Black Prism": "Black Prism",
		"Theatre":         "Theatre",
		"Alex":            "Alex",
		"A Novel":         "Novel",
		"An End":          "End",
		"Anvil":           "Anvil",
		"A":               "A",
		"":                "",
	}
	for in, want := range cases {
		if got := dropArticle(in); got != want {
			t.Errorf("dropArticle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalise(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Black Prism [Dramatized Adaptation]", "BlackPrism"},
		{"  The  Black  Prism  ", "TheBlackPrism"},
		{"The Black Prism: A Novel", "TheBlackPrismANovel"},
		{"", ""},
	}
	for _, c := range cases {
		if got := normalise(c.in); got != c.want {
			t.Errorf("normalise(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
