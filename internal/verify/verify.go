// Package verify turns "bytes arrived" into "this is the release you asked
// for".
//
// Pre-download, the scorer reads text a peer wrote about their share —
// evidence, not truth. Post-download, the staged files' own tags are ground
// truth: tone dumps the embedded metadata and this package compares it
// against the release the job was created for. A mismatch means the wrong
// edition (or a mislabelled share) was downloaded, and the job must not
// merge, tag or shelve it.
//
// The comparison is deliberately asymmetric: the release fields are the
// contract, the file tags are the evidence. A tag that is merely missing
// (no narrator tag on an mp3 share) is a soft miss — mp3 folder rips often
// carry no narrator tag — while a tag that contradicts the release (a
// different title, an abridged marker) is a hard failure. Only hard
// failures reject the candidate; soft misses are recorded for the audit.
package verify

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Verifier inspects staged audio files with tone.
type Verifier struct {
	// dump runs `tone dump <file> --format json` for one file and returns
	// its stdout. Injected so tests script the tool output; production
	// wires the tone runner.
	dump func(path string) (string, error)
}

// New builds a Verifier over a tone dump function.
func New(dump func(path string) (string, error)) *Verifier {
	return &Verifier{dump: dump}
}

// Report is the verdict for one staged batch. OK is always true: verify
// advises, it never gates. The reasons carry the warnings (·) and hits
// (✓) for the audit trail and the tag-review screen — the human decides
// whether the file is the book, not the matcher.
type Report struct {
	// OK is true when every inspected file passed the hard checks.
	OK bool
	// Reasons are the evidence lines, hits first — the same shape the
	// scorer renders, so the UI and audit treat both uniformly.
	Reasons []string
	// DurationMS is tone's audio.duration unit as reported, summed across
	// files. The unit is tone-version-dependent (seconds in current dumps,
	// despite the name) and is audit display only — confirm against live
	// `tone dump` output before trusting DurationString.
	DurationMS float64
	// Chapters is the chapter count of the first file that reports any —
	// for a merged single-file release this is the book's chapter count.
	Chapters int
}

// toneDoc is the subset of tone's dump JSON this package reads. Unknown
// fields are ignored: tone adds fields across versions and the verifier must
// not break on an upgrade.
type toneDoc struct {
	Audio struct {
		Duration float64 `json:"duration"`
		Bitrate  float64 `json:"bitrate"`
	} `json:"audio"`
	Meta struct {
		Title          string `json:"title"`
		Album          string `json:"album"`
		AlbumArtist    string `json:"albumArtist"`
		Artist         string `json:"artist"`
		Narrator       string `json:"narrator"`
		Publisher      string `json:"publisher"`
		PublishingDate string `json:"publishingDate"`
		Group          string `json:"group"`
		MovementName   string `json:"movementName"`
		Chapters       []struct {
			Start  float64 `json:"start"`
			Length float64 `json:"length"`
			Title  string  `json:"title"`
		} `json:"chapters"`
	} `json:"meta"`
}

// Release is the contract the staged files are checked against. It mirrors
// the fields the job row carries (store.Job) rather than importing the store
// — the verifier is a pure comparison and stays importable from anywhere.
type Release struct {
	Title          string
	Author         string
	Series         string
	SeriesSequence string
	Narrator       string
	Publisher      string
	Unabridged     bool
}

// Inspect dumps and judges every staged file.
//
// Every file is inspected so the report carries the full evidence. There
// are no failures: mismatches warn, and the warnings ride to tag review
// where the human reads them next to the proposed tags.
func (v *Verifier) Inspect(paths []string, rel Release) (Report, error) {
	var report Report
	if len(paths) == 0 {
		return report, fmt.Errorf("verify: no staged files to inspect")
	}

	for _, p := range paths {
		raw, err := v.dump(p)
		if err != nil {
			return report, fmt.Errorf("verify: dump %s: %w", p, err)
		}
		doc, err := parseDump(raw)
		if err != nil {
			return report, fmt.Errorf("verify: dump %s: %w", p, err)
		}
		report.DurationMS += doc.Audio.Duration
		if report.Chapters == 0 {
			report.Chapters = len(doc.Meta.Chapters)
		}
		report.Reasons = append(report.Reasons, judgeFile(doc, rel)...)
	}
	report.OK = true
	return report, nil
}

// parseDump decodes one tone dump document.
//
// tone emits one JSON document per file; the caller passes exactly one file,
// so exactly one document is expected. Trailing whitespace is tolerated.
func parseDump(raw string) (*toneDoc, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	var doc toneDoc
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("decode tone output: %w", err)
	}
	return &doc, nil
}

// judgeFile compares one file's tags against the release.
//
// Structural problems (✗) mean the file cannot be the release: an
// explicit abridgement marker, or a tag naming a work whose title shares
// nothing with the request. Everything else is a warning (·) or a hit
// (✓): encoder tagging is too volatile for hard gates — narrators in the
// author atom, subtitles in the title, co-narration credits — and the user
// already picked this file twice (search, then file selection). Verify
// advises; the tag-review screen is where the human decides.
func judgeFile(doc *toneDoc, rel Release) []string {
	var out []string

	// Title: the tag should name the work. The album tag is accepted as a
	// fallback because folder rips often carry the title only in album.
	// A tag naming a different work warns loudly — but never blocks: the
	// user picked this file, and encoder tagging is not ground truth.
	tagTitle := firstNonEmpty(doc.Meta.Title, doc.Meta.Album)
	if rel.Title != "" {
		switch {
		case tagTitle == "":
			out = append(out, "· no title tag (folder rips often omit it)")
		case matchesTitle(tagTitle, rel.Title):
			out = append(out, "✓ title tag matches the release")
		default:
			out = append(out, fmt.Sprintf("· title tag %q differs from release %q (check at tag review)", tagTitle, rel.Title))
		}
	}

	// Author: albumArtist is the audiobook convention; artist is the
	// fallback. Encoders routinely misfile the narrators here — sloppy
	// tagging, not the wrong book.
	tagAuthor := firstNonEmpty(doc.Meta.AlbumArtist, doc.Meta.Artist)
	if rel.Author != "" {
		switch {
		case tagAuthor == "":
			out = append(out, "· no author tag (folder rips often omit it)")
		case matchesTitle(tagAuthor, rel.Author):
			out = append(out, "✓ author tag matches the release")
		default:
			out = append(out, fmt.Sprintf("· author tag %q differs from release %q (check at tag review)", tagAuthor, rel.Author))
		}
	}

	// Narrator: the library contract sets "GraphicAudio" for GA releases.
	// A tag crediting other voices warns — a standard narration where a GA
	// dramatisation was requested may be the wrong edition — but the human
	// decides at tag review, not the gate.
	if rel.Narrator != "" {
		switch {
		case doc.Meta.Narrator == "":
			out = append(out, "· no narrator tag (shares often omit it)")
		case matchesNarrator(doc.Meta.Narrator, rel.Narrator):
			out = append(out, "✓ narrator tag matches the release")
		default:
			out = append(out, fmt.Sprintf("· narrator tag %q differs from release %q (check at tag review)",
				doc.Meta.Narrator, rel.Narrator))
		}
	}

	// Abridgement: an "abridged" marker anywhere in the tag text warns —
	// the edition may be cut — but still passes to tag review.
	if rel.Unabridged {
		hay := strings.ToLower(strings.Join([]string{
			doc.Meta.Title, doc.Meta.Album, doc.Meta.Group, doc.Meta.MovementName,
		}, " "))
		if strings.Contains(hay, "abridged") && !strings.Contains(hay, "unabridged") {
			out = append(out, "· tags mark the edition abridged; release is unabridged (check at tag review)")
		} else {
			out = append(out, "✓ tags do not contradict the unabridged release")
		}
	}

	// Publisher: soft — a mismatch is suspicious but publishers are
	// inconsistently tagged across re-issues.
	if rel.Publisher != "" && doc.Meta.Publisher != "" &&
		!strings.EqualFold(doc.Meta.Publisher, rel.Publisher) {
		out = append(out, fmt.Sprintf("· publisher tag %q differs from release %q",
			doc.Meta.Publisher, rel.Publisher))
	}

	return out
}

// matchesTitle compares a tag value against the release field tolerantly:
// case-insensitive, tolerant of the bracketed qualifiers ("[Dramatized
// Adaptation]") and punctuation catalogues and encoders disagree on, and
// tolerant of subtitles the tag adds after the release title
// ("Isles of the Emberdark: A Cosmere Novel" contains the whole request).
// The check is one-directional: the tag must contain the release title, not
// merely share words with it — a tag naming a different book still fails.
func matchesTitle(tag, want string) bool {
	t, w := normalise(tag), normalise(want)
	if t == w {
		return true
	}
	return strings.Contains(t, w)
}

// matchesNarrator reports whether the release narrator is credited anywhere
// in the tag's narrator field. Both sides split on the usual separators,
// so "Kaleo Griffith" matches "Kaleo Griffith, Jennifer Jill Araya" in
// either direction.
func matchesNarrator(tag, want string) bool {
	for _, t := range splitNames(tag) {
		for _, w := range splitNames(want) {
			if strings.EqualFold(normalise(t), normalise(w)) {
				return true
			}
		}
	}
	return false
}

// splitNames splits a credit field on the separators encoders use.
func splitNames(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		switch r {
		case ',', ';', '&', '/', '|':
			return true
		}
		return false
	})
}

// normalise folds case and strips everything catalogues vary on: bracketed
// qualifiers, punctuation and whitespace. The same blunt fold the catalog
// merge uses, so a GA-tagged file matches a clean release title.
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
		}
	}
	return b.String()
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// DurationString renders a millisecond duration for the audit trail.
func DurationString(ms float64) string {
	seconds := ms / 1000
	h := int(seconds) / 3600
	m := (int(seconds) % 3600) / 60
	s := int(seconds) % 60
	if h > 0 {
		return strconv.Itoa(h) + "h " + strconv.Itoa(m) + "m"
	}
	return strconv.Itoa(m) + "m " + strconv.Itoa(s) + "s"
}
