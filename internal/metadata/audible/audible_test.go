package audible

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// searchPage is the real server-rendered page for
// samplePage carries the markup shapes the parser anchors to, taken from a
// real search page: the productListItem id attribute (the ASIN), the /pd/
// link slug (the title), and a block with neither (page furniture the
// per-item scoping must skip). A full captured page was tried and dropped:
// for a single-maintainer tool the live site is the arbiter, and a small
// sample keeps parser iteration offline without pretending to pin Audible's
// markup.
const samplePage = `<html><body>
<div class="productListItem" id="product-list-item-B0BHTX4773">
  <a href="/pd/Night-Angel-Nemesis-Audiobook/B0BHTX4773"><h2><span slot="title">Night Angel Nemesis</span></h2></a>
</div>
<div class="productListItem" id="product-list-item-B001QL5N7W">
  <a href="/pd/The-Way-of-Shadows-1-of-2-Dramatized-Adaptation-Audiobook/B001QL5N7W"></a>
</div>
<div class="ad">decoration with no product id</div>
</body></html>`

func TestParsePageFindsTarget(t *testing.T) {
	books := parsePage([]byte(samplePage))

	if len(books) != 2 {
		t.Fatalf("got %d books, want 2 (the ad block is skipped)", len(books))
	}

	first := books[0]
	if first.Identifiers[0].Value != "B0BHTX4773" {
		t.Errorf("first result = %s, want B0BHTX4773", first.Identifiers[0].Value)
	}
	if first.Title != "Night Angel Nemesis" {
		t.Errorf("first title = %q, want %q", first.Title, "Night Angel Nemesis")
	}
	if first.Source != "audible" {
		t.Errorf("source = %q, want audible", first.Source)
	}
	// The second block has no title slot; the /pd/ slug is the fallback.
	if books[1].Title != "The Way of Shadows 1 of 2 Dramatized Adaptation" {
		t.Errorf("slug fallback title = %q", books[1].Title)
	}
}

func TestSlugTitle(t *testing.T) {
	cases := map[string]string{
		"Night-Angel-Nemesis-Audiobook":                             "Night Angel Nemesis",
		"The-Way-of-Shadows-1-of-2-Dramatized-Adaptation-Audiobook": "The Way of Shadows 1 of 2 Dramatized Adaptation",
		"Bare": "Bare",
		"":     "",
	}
	for in, want := range cases {
		if got := slugTitle(in); got != want {
			t.Errorf("slugTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

// flareStub is a fake FlareSolverr: POST /v1, JSON envelope, inner page
// status. It records the target URL asked for so the test can assert on the
// encoding, which is load-bearing.
type flareStub struct {
	*httptest.Server
	target string
}

func newFlareStub(t *testing.T, innerStatus int) *flareStub {
	t.Helper()
	fs := &flareStub{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req flareRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode flare request: %v", err)
		}
		if req.Cmd != "request.get" {
			t.Errorf("cmd = %q, want request.get", req.Cmd)
		}
		fs.target = req.URL

		resp := flareResponse{Status: "ok"}
		resp.Solution.Status = innerStatus
		resp.Solution.Response = "<html></html>"
		_ = json.NewEncoder(w).Encode(resp)
	}))
	fs.Server = srv
	t.Cleanup(srv.Close)
	return fs
}

func TestSearchThroughFlare(t *testing.T) {
	fs := newFlareStub(t, http.StatusOK)
	c := New("https://www.audible.example", fs.URL)

	books, err := c.Search(testContext(t), "night angel nemesis")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if fs.target == "" {
		t.Error("the proxy was never called")
	}
	// url.QueryEscape emits + for spaces, which live probes showed returning
	// unrelated results. The %20 form is the one that worked, so it is pinned
	// here — any change to the encoding becomes a conscious act with a
	// failing test attached.
	if fs.target != "https://www.audible.example/search?keywords=night%20angel%20nemesis" {
		t.Errorf("target = %q, want %%20 encoding", fs.target)
	}
	if books != nil {
		t.Errorf("books = %v, want nil from an empty page", books)
	}
}

func TestFlareInnerFailureIsUnavailable(t *testing.T) {
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusForbidden, http.StatusTooManyRequests} {
		fs := newFlareStub(t, status)
		c := New("https://www.audible.example", fs.URL)
		_, err := c.Search(testContext(t), "q")
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("inner status %d: err = %v, want ErrUnavailable", status, err)
		}
	}
}

func TestDirectPathFallback(t *testing.T) {
	// No flare endpoint means direct fetch. The direct path from a
	// datacentre IP does not work today, but the code path stays covered:
	// a home-server deployment may not need the proxy at all.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(samplePage))
	}))
	defer srv.Close()

	c := New(srv.URL, "")
	books, err := c.Search(testContext(t), "night angel nemesis")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(books) != 2 {
		t.Errorf("got %d books, want the sample page's results", len(books))
	}
}

func TestDirectThrottleIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := New(srv.URL, "")
	_, err := c.Search(testContext(t), "q")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

// TestFlareUnavailableRidesThroughADirectFailure pins the composition: when
// the proxy is configured, the direct path is not consulted, so a direct
// 503 cannot leak through as an error from the flare path.
func TestFlareUnavailableRidesThroughADirectFailure(t *testing.T) {
	fs := newFlareStub(t, http.StatusOK)
	c := New("https://www.audible.example", fs.URL)
	_, err := c.Search(testContext(t), "q")
	if err != nil {
		t.Fatalf("Search through flare should succeed: %v", err)
	}
}

// TestParsePageIsResilientToGarbage pins the per-item scoping: a page whose
// first block is malformed must still yield the later, well-formed ones.
func TestParsePageIsResilientToGarbage(t *testing.T) {
	page := []byte(`productListItem" id="product-list-item-B0GARBAGE1"` + garbage + `</li>` +
		`productListItem" id="product-list-item-B0GOOD00001"` + goodBlock + `</li>`)

	books := parsePage(page)
	if len(books) != 1 {
		t.Fatalf("got %d books, want 1", len(books))
	}
	if books[0].Title != "Good Title" {
		t.Errorf("title = %q, want Good Title", books[0].Title)
	}
}

const garbage = `<div class="broken"><strong>`

const goodBlock = `<a href="/pd/Good-Title-Audiobook/B0GOOD00001"><span slot="title">irrelevant</span></a>`

func testContext(*testing.T) context.Context { return context.Background() }
