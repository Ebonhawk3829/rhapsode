package graphicaudio

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Ebonhawk3829/rhapsode/internal/metadata"
)

// blackPrism is a real scraper response, captured from the running instance.
// It carries the shapes that broke the one-off migration script: a numeric
// episodeNumber, a string episodePart, a string totalParts, and three records
// that are three parts of the same book.
const blackPrism = `[
  {
    "link": "https://www.graphicaudio.net/lightbringer-saga-1-black-prism-1-of-3.html",
    "cover": "https://www.graphicaudio.net/media/catalog/product/cache/x/9781648794384_1.jpg",
    "seriesName": "Lightbringer Saga",
    "title": "Black Prism",
    "rawtitle": "Episode number 1 : Black Prism (1 of 3)",
    "episodeNumber": 1,
    "episodePart": "1",
    "episodeCode": "1.1",
    "totalParts": "3",
    "subtitle": "[Dramatized Adaptation]",
    "author": "Brent Weeks",
    "releaseDate": "2011-01-01T00:00:00.000Z",
    "isbn": "9781648794384",
    "genre": "Fantasy",
    "description": "Gavin Guile is the Prism, the most powerful man in the world.",
    "_confidence": 0.9
  },
  {
    "title": "Black Prism",
    "episodeNumber": 1,
    "episodePart": "2",
    "episodeCode": "1.2",
    "totalParts": "3",
    "isbn": "9781648794407",
    "author": "Brent Weeks",
    "seriesName": "Lightbringer Saga",
    "genre": "Fantasy",
    "releaseDate": "2011-01-01T00:00:00.000Z"
  },
  {
    "title": "Black Prism",
    "episodeNumber": 1,
    "episodePart": "3",
    "episodeCode": "1.3",
    "totalParts": "3",
    "isbn": "9781648794421",
    "author": "Brent Weeks",
    "seriesName": "Lightbringer Saga",
    "genre": "Fantasy",
    "releaseDate": "2011-01-01T00:00:00.000Z"
  }
]`

func newTestClient(t *testing.T, status int, body string) (*Client, *int) {
	t.Helper()
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if !strings.HasPrefix(r.URL.Path, "/search/") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL), &calls
}

func TestSearchReturnsEveryRecord(t *testing.T) {
	c, _ := newTestClient(t, http.StatusOK, blackPrism)

	got, err := c.Search(context.Background(), "black prism")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d books, want 3 — the three parts are three releases, not one", len(got))
	}
}

func TestSearchMapsFields(t *testing.T) {
	c, _ := newTestClient(t, http.StatusOK, blackPrism)

	got, err := c.Search(context.Background(), "black prism")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	b := got[0]

	checks := []struct {
		name string
		got  string
		want string
	}{
		{"Title", b.Title, "Black Prism"},
		{"Author", b.Author, "Brent Weeks"},
		{"Series", b.Series, "Lightbringer Saga"},
		{"SeriesSequence", b.SeriesSequence, "1"},
		{"Narrator", b.Narrator, "GraphicAudio"},
		{"Publisher", b.Publisher, "GraphicAudio"},
		{"PublishedYear", b.PublishedYear, "2011"},
		{"Source", b.Source, "graphicaudio"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}

	// Parts are recorded as releases: each part is its own ISBN, and its own
	// Book. The work/release split is what makes that correct rather than a
	// quirk of the scraper.
	if len(b.Identifiers) != 1 || b.Identifiers[0].Kind != metadata.ISBN {
		t.Errorf("Identifiers = %v, want one ISBN", b.Identifiers)
	}
	if b.Identifiers[0].Value != "9781648794384" {
		t.Errorf("ISBN = %q, want 9781648794384", b.Identifiers[0].Value)
	}
}

func TestSearchNumericEpisodeNumber(t *testing.T) {
	c, _ := newTestClient(t, http.StatusOK, blackPrism)

	got, err := c.Search(context.Background(), "black prism")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	// episodeNumber arrives as JSON number 1, not string "1". The wire type
	// is json.Number, so this does not panic testing.
	if got[0].SeriesSequence != "1" {
		t.Errorf("SeriesSequence = %q, want %q", got[0].SeriesSequence, "1")
	}
}

func TestPartsCarryTheirOwnISBN(t *testing.T) {
	c, _ := newTestClient(t, http.StatusOK, blackPrism)

	got, err := c.Search(context.Background(), "black prism")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	wantISBNs := []string{
		"9781648794384", "9781648794407", "9781648794421",
	}
	for i, want := range wantISBNs {
		if got[i].Identifiers[0].Value != want {
			t.Errorf("part %d ISBN = %q, want %q", i+1, got[i].Identifiers[0].Value, want)
		}
	}
}

func TestLookupByISBN(t *testing.T) {
	c, _ := newTestClient(t, http.StatusOK, blackPrism)

	got, err := c.Lookup(context.Background(), metadata.Identifier{Kind: metadata.ISBN, Value: "9781648794407"})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got.Identifiers[0].Value != "9781648794407" {
		t.Errorf("ISBN = %q, want the one asked for", got.Identifiers[0].Value)
	}
}

func TestLookupNotFound(t *testing.T) {
	c, _ := newTestClient(t, http.StatusOK, blackPrism)

	_, err := c.Lookup(context.Background(), metadata.Identifier{Kind: metadata.ISBN, Value: "9780000000000"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestLookupRejectsASIN(t *testing.T) {
	c, _ := newTestClient(t, http.StatusOK, blackPrism)

	_, err := c.Lookup(context.Background(), metadata.Identifier{Kind: metadata.ASIN, Value: "B0BL3YZSQY"})
	if err == nil {
		t.Fatal("Lookup accepted an ASIN, want an error")
	}
}

func TestSearchEmptyCatalogue(t *testing.T) {
	c, _ := newTestClient(t, http.StatusOK, `[]`)

	got, err := c.Search(context.Background(), "nothing")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d books, want 0", len(got))
	}
}

func TestSearch404IsEmpty(t *testing.T) {
	c, _ := newTestClient(t, http.StatusNotFound, "not found")

	got, err := c.Search(context.Background(), "nothing")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d books, want 0", len(got))
	}
}

func TestGenres(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"Fantasy", []string{"Fantasy"}},
		{"Fantasy, Sci-Fi", []string{"Fantasy", "Sci-Fi"}},
		{"Fantasy; Sci-Fi", []string{"Fantasy", "Sci-Fi"}},
		{"Fantasy|Sci-Fi", []string{"Fantasy", "Sci-Fi"}},
		{"", nil},
	}
	for _, c := range cases {
		got := genres(c.in)
		if len(got) != len(c.want) {
			t.Errorf("genres(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range c.want {
			if got[i] != c.want[i] {
				t.Errorf("genres(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}

func TestWireShapeMatchesFixture(t *testing.T) {
	var rs []record
	if err := json.Unmarshal([]byte(blackPrism), &rs); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	if len(rs) != 3 {
		t.Fatalf("decoded %d records, want 3", len(rs))
	}
	if rs[0].EpisodeNumber.String() != "1" {
		t.Errorf("EpisodeNumber = %s, want 1", rs[0].EpisodeNumber)
	}
	if rs[0].EpisodePart != "1" {
		t.Errorf("EpisodePart = %q, want 1", rs[0].EpisodePart)
	}
	if rs[0].TotalParts != "3" {
		t.Errorf("TotalParts = %q, want 3", rs[0].TotalParts)
	}
}
