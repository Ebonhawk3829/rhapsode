package audnexus

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Ebonhawk3829/rhapsode/internal/metadata"
)

// nemesis is a real Audnexus response, captured from
// https://api.audnex.us/books/B0BL3YZSQY. It carries both defensive shapes:
// seriesPrimary.position packs the series name ("1, Kylar Chronicles"), and
// genres is a list of objects rather than strings.
const nemesis = `{
  "asin": "B0BL3YZSQY",
  "authors": [{"asin": "B004N1S4QY", "name": "Brent Weeks"}],
  "copyright": 2023,
  "description": "After the war that cost him so much, Kylar Stern is broken and alone.",
  "genres": [
    {"asin": "18580606011", "name": "Science Fiction & Fantasy", "type": "genre"},
    {"asin": "18580607011", "name": "Fantasy", "type": "tag"},
    {"asin": "18580615011", "name": "Epic", "type": "tag"}
  ],
  "isbn": "9781405556927",
  "narrators": [{"name": "Simon Vance"}],
  "publisherName": "Little, Brown Audio",
  "releaseDate": "2023-04-27T00:00:00.000Z",
  "seriesPrimary": {"asin": "B006K1OM8E", "name": "Night Angel", "position": "1, Kylar Chronicles"},
  "subtitle": "Kylar Chronicles, Book 1",
  "summary": "<p><b>A LEGENDARY ASSASSIN.</b><br /> <b>AN IMPOSSIBLE HEIST.</b></p>",
  "title": "Night Angel Nemesis"
}`

func newTestClient(t *testing.T, status int, body string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/books/B0BL3YZSQY" {
			t.Errorf("path = %q, want /books/B0BL3YZSQY", got)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, "us")
}

func TestLookupParsesPackedPosition(t *testing.T) {
	c := newTestClient(t, http.StatusOK, nemesis)

	got, err := c.Lookup(context.Background(), metadata.Identifier{Kind: metadata.ASIN, Value: "B0BL3YZSQY"})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}

	// The position field is "1, Kylar Chronicles"; only the leading integer
	// is the sequence.
	if got.SeriesSequence != "1" {
		t.Errorf("SeriesSequence = %q, want %q", got.SeriesSequence, "1")
	}
	if got.Series != "Night Angel" {
		t.Errorf("Series = %q, want %q", got.Series, "Night Angel")
	}
}

func TestLookupFlattensGenres(t *testing.T) {
	c := newTestClient(t, http.StatusOK, nemesis)

	got, err := c.Lookup(context.Background(), metadata.Identifier{Kind: metadata.ASIN, Value: "B0BL3YZSQY"})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}

	want := []string{"Science Fiction & Fantasy", "Fantasy", "Epic"}
	if len(got.Genres) != len(want) {
		t.Fatalf("Genres = %v, want %v", got.Genres, want)
	}
	for i := range want {
		if got.Genres[i] != want[i] {
			t.Errorf("Genres[%d] = %q, want %q", i, got.Genres[i], want[i])
		}
	}
}

func TestLookupMapsFields(t *testing.T) {
	c := newTestClient(t, http.StatusOK, nemesis)

	got, err := c.Lookup(context.Background(), metadata.Identifier{Kind: metadata.ASIN, Value: "B0BL3YZSQY"})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}

	checks := []struct {
		name string
		got  string
		want string
	}{
		{"Title", got.Title, "Night Angel Nemesis"},
		{"Author", got.Author, "Brent Weeks"},
		{"Narrator", got.Narrator, "Simon Vance"},
		{"Publisher", got.Publisher, "Little, Brown Audio"},
		{"PublishedYear", got.PublishedYear, "2023"},
		{"Source", got.Source, "audnexus"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}

	if len(got.Identifiers) != 2 {
		t.Fatalf("Identifiers = %v, want an ASIN and an ISBN", got.Identifiers)
	}
	if got.Identifiers[0].Kind != metadata.ASIN || got.Identifiers[0].Value != "B0BL3YZSQY" {
		t.Errorf("Identifiers[0] = %+v, want the ASIN", got.Identifiers[0])
	}
	if got.Identifiers[1].Kind != metadata.ISBN || got.Identifiers[1].Value != "9781405556927" {
		t.Errorf("Identifiers[1] = %+v, want the ISBN", got.Identifiers[1])
	}
}

func TestLookupNotFound(t *testing.T) {
	c := newTestClient(t, http.StatusNotFound, `{"message":"not found"}`)

	_, err := c.Lookup(context.Background(), metadata.Identifier{Kind: metadata.ASIN, Value: "B0BL3YZSQY"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestLookupRejectsNonASIN(t *testing.T) {
	c := newTestClient(t, http.StatusOK, nemesis)

	_, err := c.Lookup(context.Background(), metadata.Identifier{Kind: metadata.ISBN, Value: "9781405556927"})
	if err == nil {
		t.Fatal("Lookup accepted an ISBN, want an error")
	}
}

func TestSearchUnsupported(t *testing.T) {
	c := newTestClient(t, http.StatusOK, nemesis)

	_, err := c.Search(context.Background(), "night angel")
	if !errors.Is(err, ErrNoSearch) {
		t.Fatalf("err = %v, want ErrNoSearch", err)
	}
}

func TestParsePosition(t *testing.T) {
	cases := []struct{ in, want string }{
		{"1, Kylar Chronicles", "1"},
		{"2", "2"},
		{"1.5", "1.5"},
		{"", ""},
		{" 3 , Something", "3"},
	}
	for _, c := range cases {
		if got := parsePosition(c.in); got != c.want {
			t.Errorf("parsePosition(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPublishedYearFallsBackToCopyright(t *testing.T) {
	if got := publishedYear("", 2019); got != "2019" {
		t.Errorf("publishedYear fallback = %q, want %q", got, "2019")
	}
	if got := publishedYear("2023-04-27T00:00:00.000Z", 0); got != "2023" {
		t.Errorf("publishedYear from date = %q, want %q", got, "2023")
	}
	if got := publishedYear("", 0); got != "" {
		t.Errorf("publishedYear with nothing = %q, want empty", got)
	}
}

func TestCleanDescriptionStripsHTML(t *testing.T) {
	got := cleanDescription("", "<p><b>Bold</b> text</p>")
	if got != "Bold text" {
		t.Errorf("cleanDescription = %q, want %q", got, "Bold text")
	}
	if got := cleanDescription("plain", "<p>ignored</p>"); got != "plain" {
		t.Errorf("cleanDescription preferred summary over description: %q", got)
	}
}

func TestUnknownFieldsAreIgnored(t *testing.T) {
	body := `{"asin":"X","title":"T","somethingNew":{"nested":true}}`
	c := newTestClient(t, http.StatusOK, body)

	got, err := c.Lookup(context.Background(), metadata.Identifier{Kind: metadata.ASIN, Value: "B0BL3YZSQY"})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got.Title != "T" {
		t.Errorf("Title = %q, want %q", got.Title, "T")
	}
}

// TestWireShapeMatchesFixture guards the fixture itself: if the captured JSON
// stops decoding into the wire type, the other tests are testing nothing.
func TestWireShapeMatchesFixture(t *testing.T) {
	var b book
	if err := json.Unmarshal([]byte(nemesis), &b); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	if b.SeriesPrimary == nil {
		t.Fatal("SeriesPrimary did not decode")
	}
	if len(b.Genres) != 3 {
		t.Fatalf("Genres decoded %d entries, want 3", len(b.Genres))
	}
}
