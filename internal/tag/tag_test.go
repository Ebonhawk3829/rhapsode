package tag

import (
	"context"
	"strings"
	"testing"
)

func TestTagMapsTheLibraryContract(t *testing.T) {
	var got []string
	tg := New()
	tg.SetRunner(func(_ context.Context, args []string) error {
		got = args
		return nil
	})

	err := tg.Tag(context.Background(), "/staging/book.m4b", Metadata{
		Title:          "The Black Prism",
		Author:         "Brent Weeks",
		Series:         "Lightbringer Saga",
		SeriesSequence: "1",
		Narrator:       "GraphicAudio",
		Publisher:      "GraphicAudio",
		PublishedYear:  "2011",
		Description:    "Gavin Guile is the Prism.",
		Genre:          "Fantasy",
	})
	if err != nil {
		t.Fatalf("Tag: %v", err)
	}

	joined := strings.Join(got, "\x00")
	checks := map[string]string{
		"--meta-title":         "The Black Prism",
		"--meta-album":         "The Black Prism",
		"--meta-album-artist":  "Brent Weeks",
		"--meta-artist":        "Brent Weeks",
		"--meta-narrator":      "GraphicAudio",
		"--meta-publisher":     "GraphicAudio",
		"--meta-movement-name": "Lightbringer Saga",
		"--meta-part":          "1",
		"--meta-description":   "Gavin Guile is the Prism.",
		"--meta-genre":         "Fantasy",
	}
	for flag, want := range checks {
		if !strings.Contains(joined, flag+"\x00"+want) {
			t.Errorf("args missing %s %q: %v", flag, want, got)
		}
	}
	// The published year is a bare YYYY on the job; tone wants a date.
	if !strings.Contains(joined, "--meta-publishing-date\x002011-01-01") {
		t.Errorf("publishing date not normalized: %v", got)
	}
}

func TestTagStandaloneOmitsSeries(t *testing.T) {
	var got []string
	tg := New()
	tg.SetRunner(func(_ context.Context, args []string) error {
		got = args
		return nil
	})

	if err := tg.Tag(context.Background(), "/staging/book.m4b", Metadata{
		Title:  "Warbreaker",
		Author: "Brandon Sanderson",
	}); err != nil {
		t.Fatalf("Tag: %v", err)
	}
	joined := strings.Join(got, "\x00")
	for _, flag := range []string{"--meta-movement-name", "--meta-part", "--meta-narrator", "--meta-publisher"} {
		if strings.Contains(joined, flag) {
			t.Errorf("standalone carries %s: %v", flag, got)
		}
	}
}

func TestTagRequiresTitleAndAuthor(t *testing.T) {
	tg := New()
	tg.SetRunner(func(context.Context, []string) error {
		t.Error("tone ran for an invalid contract")
		return nil
	})
	if err := tg.Tag(context.Background(), "/x.m4b", Metadata{Author: "A"}); err == nil {
		t.Error("missing title accepted")
	}
	if err := tg.Tag(context.Background(), "/x.m4b", Metadata{Title: "T"}); err == nil {
		t.Error("missing author accepted")
	}
}

func TestTagToolFailureSurfacesTail(t *testing.T) {
	tg := New()
	tg.SetRunner(func(context.Context, []string) error {
		return context.DeadlineExceeded
	})
	if err := tg.Tag(context.Background(), "/x.m4b", Metadata{Title: "T", Author: "A"}); err == nil {
		t.Fatal("a tool failure vanished")
	}
}
