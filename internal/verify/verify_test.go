package verify

import (
	"fmt"
	"strings"
	"testing"
)

// gaDump is a tone dump document shaped like the real GA library files
// (probed live: The Emperor's Soul — narrator/publisher "GraphicAudio",
// chapters present, duration in milliseconds).
const gaDump = `{
  "audio": {"duration": 12661277.0, "bitrate": 269},
  "meta": {
    "title": "The Emperor's Soul",
    "album": "The Emperor's Soul",
    "albumArtist": "Brandon Sanderson",
    "artist": "Brandon Sanderson",
    "narrator": "GraphicAudio",
    "publisher": "GraphicAudio",
    "publishingDate": "2019-07-23T00:00:00",
    "chapters": [
      {"start": 0, "length": 56000, "title": "Graphic Audio Intro"},
      {"start": 56000, "length": 210000, "title": "Prologue"}
    ]
  }
}`

func gaRelease() Release {
	return Release{
		Title:      "The Emperor's Soul",
		Author:     "Brandon Sanderson",
		Narrator:   "GraphicAudio",
		Publisher:  "GraphicAudio",
		Unabridged: true,
	}
}

func verifierWith(dump string) *Verifier {
	return New(func(string) (string, error) { return dump, nil })
}

func TestInspectAcceptsTheRightRelease(t *testing.T) {
	report, err := verifierWith(gaDump).Inspect([]string{"/staged/book.m4b"}, gaRelease())
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !report.OK {
		t.Errorf("report not OK: %v", report.Reasons)
	}
	if report.DurationMS != 12661277.0 {
		t.Errorf("duration = %v", report.DurationMS)
	}
	if report.Chapters != 2 {
		t.Errorf("chapters = %d, want 2", report.Chapters)
	}
}

func TestInspectRejectsWrongTitle(t *testing.T) {
	wrong := strings.Replace(gaDump, `"The Emperor's Soul",`, `"Some Other Book",`, 1)
	report, err := verifierWith(wrong).Inspect([]string{"/staged/book.m4b"}, gaRelease())
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if report.OK {
		t.Errorf("a wrong-title download passed: %v", report.Reasons)
	}
	joined := strings.Join(report.Reasons, ";")
	if !strings.Contains(joined, "title tag") {
		t.Errorf("verdict does not name the title mismatch: %v", report.Reasons)
	}
}

func TestInspectRejectsWrongNarrator(t *testing.T) {
	// A standard narration tagged where a GA dramatisation was requested is
	// the wrong edition — hard failure, not a soft miss.
	wrong := strings.Replace(gaDump, `"narrator": "GraphicAudio"`, `"narrator": "Simon Vance"`, 1)
	report, err := verifierWith(wrong).Inspect([]string{"/staged/book.m4b"}, gaRelease())
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if report.OK {
		t.Errorf("a wrong-edition download passed: %v", report.Reasons)
	}
}

func TestInspectToleratesMissingNarratorTag(t *testing.T) {
	// mp3 folder rips commonly carry no narrator tag: absence is a soft
	// miss (·), never a rejection.
	missing := strings.Replace(gaDump, `"narrator": "GraphicAudio",`, `"narrator": "",`, 1)
	report, err := verifierWith(missing).Inspect([]string{"/staged/book.m4b"}, gaRelease())
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !report.OK {
		t.Errorf("a missing narrator tag rejected the download: %v", report.Reasons)
	}
	if !strings.Contains(strings.Join(report.Reasons, ";"), "no narrator tag") {
		t.Errorf("soft miss not recorded: %v", report.Reasons)
	}
}

func TestInspectRejectsAbridgedEdition(t *testing.T) {
	abridged := strings.Replace(gaDump, `"title": "The Emperor's Soul"`, `"title": "The Emperor's Soul (Abridged)"`, 1)
	report, err := verifierWith(abridged).Inspect([]string{"/staged/book.m4b"}, gaRelease())
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if report.OK {
		t.Errorf("an abridged edition passed: %v", report.Reasons)
	}
}

func TestInspectToleratesBracketedTitleQualifiers(t *testing.T) {
	// GA encoders tag "[Dramatized Adaptation]" qualifiers into titles; the
	// comparison folds them, exactly like the catalog merge does.
	qualified := strings.Replace(gaDump, `"The Emperor's Soul",`, `"The Emperor's Soul [Dramatized Adaptation]",`, 1)
	report, err := verifierWith(qualified).Inspect([]string{"/staged/book.m4b"}, gaRelease())
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !report.OK {
		t.Errorf("a bracket-qualified title rejected: %v", report.Reasons)
	}
}

func TestInspectAcceptsSubtitleExtendedTitle(t *testing.T) {
	// The embedded title may carry a subtitle the catalogue omits
	// ("Isles of the Emberdark: A Cosmere Novel" for the request "Isles of
	// the Emberdark"): the whole request is contained in the tag, so it
	// passes. A tag naming a different book still fails.
	extended := strings.Replace(gaDump, `"The Emperor's Soul",`, `"The Emperor's Soul: A Cosmere Short Story",`, 1)
	report, err := verifierWith(extended).Inspect([]string{"/staged/book.m4b"}, gaRelease())
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !report.OK {
		t.Errorf("a subtitle-extended title rejected: %v", report.Reasons)
	}
}

func TestInspectAcceptsCoNarratedTag(t *testing.T) {
	// Dual narration credits both voices; the release names one. Any
	// overlap passes — the check catches wrong editions, not credits.
	co := strings.Replace(gaDump, `"narrator": "Sasha Morton",`, `"narrator": "Sasha Morton, William Demeritt",`, 1)
	report, err := verifierWith(co).Inspect([]string{"/staged/book.m4b"}, gaRelease())
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !report.OK {
		t.Errorf("a co-narrated tag rejected: %v", report.Reasons)
	}
}

func TestInspectEveryFileMustPass(t *testing.T) {
	// A batch with one wrong-titled part is a wrong download, not a partial
	// success.
	dumps := map[string]string{
		"/staged/part1.m4b": gaDump,
		"/staged/part2.m4b": strings.Replace(gaDump, `"The Emperor's Soul",`, `"Wrong Book",`, 1),
	}
	v := New(func(path string) (string, error) { return dumps[path], nil })
	report, err := v.Inspect([]string{"/staged/part1.m4b", "/staged/part2.m4b"}, gaRelease())
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if report.OK {
		t.Errorf("a batch with one wrong part passed: %v", report.Reasons)
	}
}

func TestInspectEmptyBatchFails(t *testing.T) {
	if _, err := verifierWith(gaDump).Inspect(nil, gaRelease()); err == nil {
		t.Fatal("an empty batch inspected cleanly, want an error")
	}
}

func TestInspectDumpErrorSurfaces(t *testing.T) {
	v := New(func(string) (string, error) { return "", fmt.Errorf("tone exploded") })
	if _, err := v.Inspect([]string{"/staged/book.m4b"}, gaRelease()); err == nil {
		t.Fatal("a dump error vanished, want it surfaced")
	}
}

func TestParseDumpIgnoresUnknownFields(t *testing.T) {
	// tone adds fields across versions; the parser must not break on an
	// upgrade.
	extra := strings.Replace(gaDump, `"audio": {`, `"audio": {"someFutureField": 1,`, 1)
	doc, err := parseDump(extra)
	if err != nil {
		t.Fatalf("parseDump: %v", err)
	}
	if doc.Meta.Title != "The Emperor's Soul" {
		t.Errorf("title = %q", doc.Meta.Title)
	}
}

func TestDurationString(t *testing.T) {
	cases := map[float64]string{
		12661277.0: "3h 31m",
		5969733.0:  "1h 39m",
		125000.0:   "2m 5s",
	}
	for in, want := range cases {
		if got := DurationString(in); got != want {
			t.Errorf("DurationString(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestAuthorTagNamingNarratorsIsSoft(t *testing.T) {
	// Encoders misfile narrators into the author atom. That is sloppy
	// tagging, not the wrong book: soft, never a rejection.
	swapped := strings.Replace(gaDump, `"albumArtist": "Brandon Sanderson"`, `"albumArtist": "GraphicAudio"`, 1)
	rel := gaRelease()
	report, err := verifierWith(swapped).Inspect([]string{"/staged/book.m4b"}, rel)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !report.OK {
		t.Errorf("narrators-in-author rejected: %v", report.Reasons)
	}
}
