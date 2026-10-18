// Package score grades candidate offers against the release they were
// searched for.
//
// The score orders the confirm screen — most likely first — and the reasons
// are the evidence lines shown under each row. It is a display aid, never a
// gate: no band, score or threshold removes a candidate from what the user
// can see or choose, because a peer's directory text is not ground truth and
// the user is the judge of what satisfies the request.
package score

import (
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/Ebonhawk3829/rhapsode/internal/source"
)

// Bands summarise a grade for the confirm screen. They are labels, not
// filters: a reject-band candidate is still shown and still selectable.
const (
	BandAccept = "accept"
	BandReview = "review"
	BandReject = "reject"
)

// Result is the grade of one candidate.
type Result struct {
	Score   float64 // 0..1, informational; ordering comes from sort
	Band    string
	Reasons []string // hits then misses; rendered verbatim
}

// Grade scores one candidate against the release it was found for.
//
// Text evidence comes from the offer title, the remote directory and the
// filenames; audio facts (format, bitrate) from the candidate's own summary.
// Everything is matched case-insensitively because peers do not agree on
// punctuation or case.
func Grade(rel source.Release, c source.Candidate) Result {
	hay := strings.ToLower(strings.Join([]string{c.Title, dirOf(c), fileBlob(c)}, " "))

	var pos, neg []string
	possible, gained := 0.0, 0.0

	judge := func(ok bool, weight float64, hit, miss string) {
		possible += weight
		if ok {
			gained += weight
			if hit != "" {
				pos = append(pos, "✓ "+hit)
			}
		} else {
			if miss != "" {
				neg = append(neg, "✗ "+miss)
			}
		}
	}

	// Text is the primary evidence: it is what a peer chose to name the
	// share, and the only signal that can distinguish editions.
	if rel.Title != "" {
		judge(contains(hay, rel.Title), 3.0,
			"title in share", "title not found in share text")
	}
	if rel.Author != "" {
		judge(contains(hay, rel.Author), 2.0,
			"author in share", "author not found")
	}

	// GraphicAudio is a house style: its shares say "GraphicAudio" or
	// "Dramatized" rather than a narrator's name. Any other release looks
	// for its narrator by name.
	if rel.Narrator == "GraphicAudio" {
		judge(containsAny(hay, "graphic audio", "graphicaudio", "dramatiz", "dramatis"), 2.5,
			"dramatised-adaptation marker present",
			"no dramatised-adaptation marker (release is GraphicAudio)")
	} else if rel.Narrator != "" {
		judge(contains(hay, rel.Narrator), 1.5,
			"narrator "+rel.Narrator+" in share",
			"narrator not in share text")
	}

	// Abridgement is the strongest edition mismatch. It survives sloppy
	// naming because a peer who owned the unabridged files rarely relabelled
	// them; "abridged" present without "unabridged" is therefore a real
	// signal, not a coincidence of substring matching.
	if rel.Unabridged && contains(hay, "abridged") && !contains(hay, "unabridged") {
		neg = append(neg, "✗ share is marked abridged; release is unabridged")
		possible += 4.0
	} else if rel.Unabridged {
		pos = append(pos, "✓ not marked abridged")
		gained += 0.5
		possible += 0.5
	}

	// Part structure: a multi-part release should see part/disc markers in
	// the share. A single-part release is never penalised for a multi-file
	// disc rip, which is a normal single-narration shape.
	if rel.PartCount > 1 {
		found := len(distinct(parts(c)))
		judge(found > 1, 2.0,
			fmt.Sprintf("part structure found (%d distinct)", found),
			fmt.Sprintf("no part breakdown in share; release has %d parts", rel.PartCount))
	}

	// Format: m4b/m4a is the library target; other formats are tolerated and
	// shown as such rather than punished bluntly.
	switch c.Format {
	case "m4b", "m4a":
		gained += 1.0
		possible += 1.0
		pos = append(pos, "✓ "+c.Format+" format")
	case "mp3":
		gained += 0.5
		possible += 1.0
		pos = append(pos, "✓ mp3 (tolerated)")
	case "flac":
		gained += 0.25
		possible += 1.0
		pos = append(pos, "✓ flac (lossless)")
	default:
		possible += 1.0
		neg = append(neg, "✗ unknown format")
	}

	// These markers mean "not the wanted audio" whatever else looked right.
	for _, m := range []string{"sample", "preview", "excerpt"} {
		if contains(hay, m) {
			neg = append(neg, "✗ share references a "+m)
			possible += 2.0
		}
	}

	s := 0.0
	if possible > 0 {
		s = gained / possible
	}
	return Result{Score: s, Band: bandFor(s, neg), Reasons: append(pos, neg...)}
}

func bandFor(s float64, neg []string) string {
	switch {
	case s >= 0.66 && len(neg) == 0:
		return BandAccept
	case s >= 0.45:
		return BandReview
	default:
		return BandReject
	}
}

// dirOf is the peer-side directory the share lives in. It falls back to the
// stored filename path when no directory was recorded separately.
func dirOf(c source.Candidate) string {
	if c.RemoteDir != "" {
		return c.RemoteDir
	}
	if len(c.Files) == 0 {
		return ""
	}
	return path.Dir(c.Files[0].Name)
}

func fileBlob(c source.Candidate) string {
	names := make([]string, 0, len(c.Files))
	for _, f := range c.Files {
		names = append(names, f.Name)
	}
	return strings.Join(names, " ")
}

func contains(hay, needle string) bool {
	return strings.Contains(hay, strings.ToLower(needle))
}

func containsAny(hay string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(hay, n) {
			return true
		}
	}
	return false
}

var partRe = regexp.MustCompile(`(?:part|disc|cd)[ ._\-]?0*([0-9]{1,2})\b`)

// parts digs part numbers out of filenames; they feed the multi-part check.
func parts(c source.Candidate) []int {
	var out []int
	for _, f := range c.Files {
		for _, m := range partRe.FindAllStringSubmatch(strings.ToLower(f.Name), -1) {
			if n, err := strconv.Atoi(m[1]); err == nil {
				out = append(out, n)
			}
		}
	}
	return out
}

// distinct de-duplicates part numbers so "part 1" matching inside "part 11"
// cannot inflate the count.
func distinct(parts []int) []int {
	seen := map[int]bool{}
	var out []int
	for _, p := range parts {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}
