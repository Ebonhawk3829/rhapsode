package score

import (
	"strings"
	"testing"

	"github.com/Ebonhawk3829/rhapsode/internal/source"
)

func relGA() source.Release {
	return source.Release{
		Title:      "The Way of Kings",
		Author:     "Brandon Sanderson",
		Narrator:   "GraphicAudio",
		Unabridged: true,
		PartCount:  3,
	}
}

func candGA(parts int) source.Candidate {
	files := make([]source.File, 0, parts)
	for i := 1; i <= parts; i++ {
		files = append(files, source.File{
			Name: "Brandon Sanderson/The Way of Kings Part " + itoa(i) + " [GraphicAudio] 256kbps.m4b",
			Size: 300 << 20,
		})
	}
	return source.Candidate{
		Title:     "The Way of Kings",
		Format:    "m4b",
		SizeBytes: int64(parts) * (300 << 20),
		Files:     files,
		Handle:    source.Handle{Kind: source.P2P},
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

func TestGradeFavoursFullMatch(t *testing.T) {
	r := Grade(relGA(), candGA(3))
	if r.Band != BandAccept {
		t.Errorf("band = %q (%v), want accept", r.Band, r.Reasons)
	}
	for _, want := range []string{"title", "author", "dramatised"} {
		if !strings.Contains(strings.Join(r.Reasons, ";"), want) {
			t.Errorf("reasons missing %q: %v", want, r.Reasons)
		}
	}
}

func TestGradeFlagsWrongEdition(t *testing.T) {
	c := candGA(1)
	c.Files = []source.File{{Name: "The Way of Kings (Abridged).mp3", Size: 200 << 20}}
	c.Format = "mp3"
	r := Grade(relGA(), c)
	if r.Band == BandAccept {
		t.Errorf("an abridged share rated %q: %v", r.Band, r.Reasons)
	}
	found := false
	for _, reason := range r.Reasons {
		if strings.Contains(reason, "abridged") {
			found = true
		}
	}
	if !found {
		t.Errorf("abridgement contradiction not reported: %v", r.Reasons)
	}
}

func TestGradeOrdersLikelihood(t *testing.T) {
	good := Grade(relGA(), candGA(3))
	bad := Grade(relGA(), source.Candidate{
		Title:  "Some Other Book",
		Format: "mp3",
		Files:  []source.File{{Name: "Someone Else/Unrelated Book.mp3"}},
		Handle: source.Handle{Kind: source.P2P},
	})
	if good.Score <= bad.Score {
		t.Errorf("full match %v did not outrank unrelated share %v", good.Score, bad.Score)
	}
}

func TestGradeNeverGates(t *testing.T) {
	// A terrible candidate still gets a complete verdict, not a hiding:
	// the confirm screen decides what is shown, and the user decides what
	// to do with it.
	bad := Grade(relGA(), source.Candidate{Format: "mp3", Handle: source.Handle{Kind: source.P2P}})
	if bad.Band == "" || len(bad.Reasons) == 0 {
		t.Fatalf("worst-case grade lost its reasons: %+v", bad)
	}
}

func TestPartDetection(t *testing.T) {
	got := distinct(parts(candGA(3)))
	if len(got) != 3 {
		t.Errorf("found %d distinct parts, want 3: %v", len(got), got)
	}
	// "Part 11" must not add "Part 1".
	dupCand := source.Candidate{Files: []source.File{{Name: "Part 11 of 12.m4b"}}}
	if n := len(distinct(parts(dupCand))); n != 1 {
		t.Errorf("part 11 parsed as %d parts", n)
	}
}
