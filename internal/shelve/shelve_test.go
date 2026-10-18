package shelve

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLibraryPathSeriesBook(t *testing.T) {
	got, err := LibraryPath("/library", Tagged{
		Title:  "The Black Prism",
		Author: "Brent Weeks",
		Series: "Lightbringer Saga",
	})
	if err != nil {
		t.Fatalf("LibraryPath: %v", err)
	}
	want := "/library/Brent Weeks/Lightbringer Saga/The Black Prism"
	if got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}

func TestLibraryPathStandalone(t *testing.T) {
	got, err := LibraryPath("/library", Tagged{
		Title:  "Warbreaker",
		Author: "Brandon Sanderson",
	})
	if err != nil {
		t.Fatalf("LibraryPath: %v", err)
	}
	want := "/library/Brandon Sanderson/Warbreaker"
	if got != want {
		t.Errorf("path = %q, want %q (no Stand-Alone placeholder)", got, want)
	}
}

func TestLibraryPathCleanSegments(t *testing.T) {
	got, err := LibraryPath("/library", Tagged{
		Title:  "Black Prism [Dramatized Adaptation]",
		Author: "Brent Weeks",
		Series: "Lightbringer Saga #1",
	})
	if err != nil {
		t.Fatalf("LibraryPath: %v", err)
	}
	// No brackets, no '#': the contract forbids both in paths.
	if strings.ContainsAny(got, "[]#") {
		t.Errorf("path carries forbidden characters: %q", got)
	}
	if !strings.Contains(got, "Lightbringer Saga") {
		t.Errorf("series segment mangled: %q", got)
	}
}

func TestLibraryPathKeepsApostrophes(t *testing.T) {
	got, err := LibraryPath("/library", Tagged{
		Title:  "The Emperor's Soul",
		Author: "Brandon Sanderson",
	})
	if err != nil {
		t.Fatalf("LibraryPath: %v", err)
	}
	if !strings.Contains(got, "The Emperor's Soul") {
		t.Errorf("apostrophe dropped: %q", got)
	}
}

func TestLibraryPathRequiresAuthorAndTitle(t *testing.T) {
	if _, err := LibraryPath("/library", Tagged{Title: "T"}); err == nil {
		t.Error("missing author accepted")
	}
	if _, err := LibraryPath("/library", Tagged{Author: "A"}); err == nil {
		t.Error("missing title accepted")
	}
}

func TestShelveCopiesAndRenamesAtomically(t *testing.T) {
	library := t.TempDir()
	src := filepath.Join(t.TempDir(), "book.m4b")
	if err := os.WriteFile(src, []byte("tagged-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	var scanned bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/library/lib-1/scan" && r.Method == http.MethodPost {
			scanned = true
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	s := New(library, srv.URL, "token", "lib-1")
	dest, err := s.Shelve(context.Background(), src, Tagged{
		Title:  "The Black Prism",
		Author: "Brent Weeks",
		Series: "Lightbringer Saga",
	})
	if err != nil {
		t.Fatalf("Shelve: %v", err)
	}
	if !scanned {
		t.Error("ABS scan was not called")
	}
	if dest != filepath.Join(library, "Brent Weeks", "Lightbringer Saga", "The Black Prism", "The Black Prism.m4b") {
		t.Errorf("dest = %q", dest)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read shelved file: %v", err)
	}
	if string(got) != "tagged-bytes" {
		t.Error("shelved bytes differ")
	}
	// No .part leftovers.
	entries, _ := os.ReadDir(filepath.Dir(dest))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".part") {
			t.Errorf("temporary .part file left behind: %s", e.Name())
		}
	}
}

func TestShelveScanFailureKeepsTheBook(t *testing.T) {
	library := t.TempDir()
	src := filepath.Join(t.TempDir(), "book.m4b")
	if err := os.WriteFile(src, []byte("bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	s := New(library, srv.URL, "token", "lib-1")
	dest, err := s.Shelve(context.Background(), src, Tagged{Title: "Book", Author: "A"})
	if err == nil {
		t.Fatal("a failed scan vanished")
	}
	if !strings.Contains(err.Error(), "scan") {
		t.Errorf("error does not name the scan: %v", err)
	}
	// The book is in the library regardless — ABS picks it up next scan.
	if _, statErr := os.Stat(dest); statErr != nil {
		t.Errorf("the shelved book did not survive the scan failure: %v", statErr)
	}
}

func TestAlreadyOwnedMatchesCleanTitles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"book": [{"libraryItem": {"media": {"metadata": {"title": "Black Prism [Dramatized Adaptation]"}}}}]}`))
	}))
	defer srv.Close()

	s := New("/library", srv.URL, "token", "lib-1")
	owned, err := s.AlreadyOwned(context.Background(), "The Black Prism")
	if err != nil {
		t.Fatalf("AlreadyOwned: %v", err)
	}
	if !owned {
		t.Error("an owned title (with qualifier) was not recognised")
	}
}

func TestAlreadyOwnedErrorIsNotSilent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	s := New("/library", srv.URL, "token", "lib-1")
	if _, err := s.AlreadyOwned(context.Background(), "Book"); err == nil {
		t.Fatal("an unreachable ABS read as 'not owned' — duplicates would follow")
	}
}
