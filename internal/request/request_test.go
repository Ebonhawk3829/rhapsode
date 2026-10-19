package request

import (
	"strings"
	"testing"

	"github.com/Ebonhawk3829/rhapsode/internal/store"
)

func TestSlugDeterministic(t *testing.T) {
	a := Slug(Release{Title: "The Black Prism", Identifier: ID{Kind: "isbn", Value: "9781648794384"}})
	b := Slug(Release{Title: "The Black Prism", Identifier: ID{Kind: "isbn", Value: "9781648794384"}})
	if a != b {
		t.Errorf("slug not deterministic: %q vs %q", a, b)
	}
	if !strings.HasPrefix(a, "the-black-prism-") {
		t.Errorf("slug = %q, want human-readable title prefix", a)
	}
}

func TestSlugDistinctReleases(t *testing.T) {
	a := Slug(Release{Title: "The Black Prism", Identifier: ID{Kind: "isbn", Value: "1"}})
	b := Slug(Release{Title: "The Black Prism", Identifier: ID{Kind: "isbn", Value: "2"}})
	if a == b {
		t.Errorf("distinct releases share slug %q", a)
	}
}

func TestSlugQueryFallback(t *testing.T) {
	a := Slug(Release{Title: "Unknown Book", Query: "some raw query"})
	if !strings.HasPrefix(a, "unknown-book-") {
		t.Errorf("slug = %q, want title prefix with query hash", a)
	}
	b := Slug(Release{Title: "Unknown Book", Query: "some raw query"})
	if a != b {
		t.Errorf("query slug not resumable: %q vs %q", a, b)
	}
}

func TestSlugEmptyTitle(t *testing.T) {
	got := Slug(Release{Title: "!!!", Identifier: ID{Kind: "isbn", Value: "1"}})
	if !strings.HasPrefix(got, "request-") {
		t.Errorf("slug = %q, want request- fallback", got)
	}
}

func TestDuplicateLiveJobRefused(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c := New(st)
	rel := Release{Title: "The Black Prism", Identifier: ID{Kind: "isbn", Value: "1"}}
	first, err := c.Request(rel)
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	if _, err := c.Request(rel); err == nil {
		t.Fatal("second live request accepted, want refusal")
	} else if !strings.Contains(err.Error(), first.ID) {
		t.Errorf("error = %q, want it to name the existing job", err.Error())
	}
	// A terminal job frees the slug: re-request after failure starts fresh.
	first.State = store.Failed
	if err := st.Update(first); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Request(rel); err != nil {
		t.Errorf("re-request after terminal = %v, want success", err)
	}
}
